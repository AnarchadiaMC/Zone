package network

import (
	"net"
	"sync"
	"time"
)

// AckEntry represents a single unacknowledged packet awaiting confirmation.
type AckEntry struct {
	Seq        uint32
	Addr       *net.UDPAddr
	Data       []byte
	EnqueuedAt time.Time
	LastSentAt time.Time
	Retries    int
	MaxRetries int
}

// AckQueue tracks reliable packets awaiting acknowledgment and handles
// periodic retransmission and retry timeout drops.
type AckQueue struct {
	mu                 sync.RWMutex
	entries            map[uint32]*AckEntry
	pending            map[uint32]*AckEntry // backwards-compatibility alias
	retransmitInterval time.Duration
	maxRetries         int
	sendFunc           func(addr *net.UDPAddr, data []byte) error
	onDrop             func(seq uint32, addr *net.UDPAddr)
}

// NewAckQueue creates a new AckQueue. It supports both the full 3-argument signature:
//
//	NewAckQueue(retransmitInterval, maxRetries, sendFunc)
//
// and a 0-argument default invocation for backwards compatibility with existing call sites.
func NewAckQueue(args ...any) *AckQueue {
	retransmitInterval := 500 * time.Millisecond
	maxRetries := 5
	var sendFunc func(addr *net.UDPAddr, data []byte) error

	if len(args) >= 1 {
		if d, ok := args[0].(time.Duration); ok && d > 0 {
			retransmitInterval = d
		}
	}
	if len(args) >= 2 {
		if r, ok := args[1].(int); ok && r > 0 {
			maxRetries = r
		}
	}
	if len(args) >= 3 {
		if fn, ok := args[2].(func(*net.UDPAddr, []byte) error); ok {
			sendFunc = fn
		}
	}

	entries := make(map[uint32]*AckEntry)
	return &AckQueue{
		entries:            entries,
		pending:            entries,
		retransmitInterval: retransmitInterval,
		maxRetries:         maxRetries,
		sendFunc:           sendFunc,
	}
}

// SetOnDrop sets an optional callback invoked when a packet exceeds maxRetries.
func (aq *AckQueue) SetOnDrop(cb func(seq uint32, addr *net.UDPAddr)) {
	aq.mu.Lock()
	defer aq.mu.Unlock()
	aq.onDrop = cb
}

// SetSendFunc sets the sender function used by Tick for retransmissions.
func (aq *AckQueue) SetSendFunc(sendFunc func(addr *net.UDPAddr, data []byte) error) {
	aq.mu.Lock()
	defer aq.mu.Unlock()
	aq.sendFunc = sendFunc
}

// Enqueue adds a reliable packet to the queue awaiting acknowledgment.
func (aq *AckQueue) Enqueue(seq uint32, addr *net.UDPAddr, data []byte) {
	b := make([]byte, len(data))
	copy(b, data)

	now := time.Now()
	entry := &AckEntry{
		Seq:        seq,
		Addr:       addr,
		Data:       b,
		EnqueuedAt: now,
		LastSentAt: now,
		Retries:    0,
		MaxRetries: aq.maxRetries,
	}

	aq.mu.Lock()
	defer aq.mu.Unlock()
	aq.entries[seq] = entry
}

// Acknowledge removes the packet with the specified sequence number from the queue.
// Returns true if the packet was found and removed, false otherwise.
// It is address-blind; network callers must use AcknowledgeFrom so one client
// cannot clear another client's retransmit entry.
func (aq *AckQueue) Acknowledge(seq uint32) bool {
	aq.mu.Lock()
	defer aq.mu.Unlock()
	if _, exists := aq.entries[seq]; exists {
		delete(aq.entries, seq)
		return true
	}
	return false
}

// AcknowledgeFrom removes the packet with the given sequence number only when
// the pending entry was enqueued for addr. Sequence numbers are one global
// space, so an address-blind ACK would let any client suppress another
// client's reliable retransmissions. Returns true when the entry was removed.
func (aq *AckQueue) AcknowledgeFrom(seq uint32, addr *net.UDPAddr) bool {
	if addr == nil {
		return false
	}
	aq.mu.Lock()
	defer aq.mu.Unlock()
	entry, exists := aq.entries[seq]
	if !exists || !sameUDPAddr(entry.Addr, addr) {
		return false
	}
	delete(aq.entries, seq)
	return true
}

// sameUDPAddr reports whether two UDP endpoints identify the same peer.
func sameUDPAddr(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Port == b.Port && a.Zone == b.Zone && a.IP.Equal(b.IP)
}

// Tick checks unacknowledged packets and retransmits any whose retransmission
// interval has expired. If a packet has reached or exceeded maxRetries, it is
// removed from the queue and onDrop is called. Returns the list of dropped sequence numbers.
func (aq *AckQueue) Tick(now time.Time) []uint32 {
	type dropItem struct {
		seq  uint32
		addr *net.UDPAddr
	}
	type sendItem struct {
		addr *net.UDPAddr
		data []byte
	}

	var toDrop []dropItem
	var toSend []sendItem
	var droppedSeqs []uint32

	aq.mu.Lock()
	for seq, entry := range aq.entries {
		if now.Sub(entry.LastSentAt) >= aq.retransmitInterval {
			if entry.Retries >= entry.MaxRetries {
				delete(aq.entries, seq)
				droppedSeqs = append(droppedSeqs, seq)
				if aq.onDrop != nil {
					toDrop = append(toDrop, dropItem{seq: seq, addr: entry.Addr})
				}
			} else {
				entry.Retries++
				entry.LastSentAt = now
				if aq.sendFunc != nil {
					toSend = append(toSend, sendItem{addr: entry.Addr, data: entry.Data})
				}
			}
		}
	}
	dropCb := aq.onDrop
	sendFn := aq.sendFunc
	aq.mu.Unlock()

	// Execute callbacks and sends outside the lock to prevent deadlocks
	for _, item := range toDrop {
		if dropCb != nil {
			dropCb(item.seq, item.addr)
		}
	}
	for _, item := range toSend {
		if sendFn != nil {
			_ = sendFn(item.addr, item.data)
		}
	}

	return droppedSeqs
}

// RemoveByAddr removes all pending packets destined for the specified address string.
func (aq *AckQueue) RemoveByAddr(addr string) {
	aq.mu.Lock()
	defer aq.mu.Unlock()
	for seq, entry := range aq.entries {
		if entry.Addr != nil && entry.Addr.String() == addr {
			delete(aq.entries, seq)
		}
	}
}

// Len returns the current number of unacknowledged packets in the queue.
func (aq *AckQueue) Len() int {
	aq.mu.RLock()
	defer aq.mu.RUnlock()
	return len(aq.entries)
}

// Clear removes all entries from the queue.
func (aq *AckQueue) Clear() {
	aq.mu.Lock()
	defer aq.mu.Unlock()
	aq.entries = make(map[uint32]*AckEntry)
	aq.pending = aq.entries
}

// GetEntry returns a copy of the AckEntry for the given sequence number, if present.
func (aq *AckQueue) GetEntry(seq uint32) (AckEntry, bool) {
	aq.mu.RLock()
	defer aq.mu.RUnlock()
	entry, ok := aq.entries[seq]
	if !ok {
		return AckEntry{}, false
	}
	return *entry, true
}

// --- Backwards Compatibility Methods ---

// EnqueueReliable provides backwards-compatible naming for Enqueue.
func (aq *AckQueue) EnqueueReliable(seq uint32, addr *net.UDPAddr, data []byte) {
	aq.Enqueue(seq, addr, data)
}

// AckReceived provides backwards-compatible naming for Acknowledge.
func (aq *AckQueue) AckReceived(seq uint32) {
	aq.Acknowledge(seq)
}
