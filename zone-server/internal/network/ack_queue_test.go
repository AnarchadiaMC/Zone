package network

import (
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"
)

func TestAckQueue_EnqueueAndAcknowledge(t *testing.T) {
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:27015")
	if err != nil {
		t.Fatalf("ResolveUDPAddr failed: %v", err)
	}

	aq := NewAckQueue(500*time.Millisecond, 5, nil)
	if aq.Len() != 0 {
		t.Fatalf("Expected empty queue, got len=%d", aq.Len())
	}

	payload := []byte("packet-data-1")
	aq.Enqueue(1001, addr, payload)

	if aq.Len() != 1 {
		t.Fatalf("Expected Len() == 1, got %d", aq.Len())
	}

	entry, found := aq.GetEntry(1001)
	if !found {
		t.Fatalf("Expected entry 1001 to be found")
	}
	if entry.Seq != 1001 {
		t.Errorf("Expected Seq=1001, got %d", entry.Seq)
	}
	if string(entry.Data) != string(payload) {
		t.Errorf("Expected Data=%s, got %s", payload, string(entry.Data))
	}
	if entry.Retries != 0 {
		t.Errorf("Expected Retries=0, got %d", entry.Retries)
	}
	if entry.MaxRetries != 5 {
		t.Errorf("Expected MaxRetries=5, got %d", entry.MaxRetries)
	}

	// Immediate acknowledge
	acked := aq.Acknowledge(1001)
	if !acked {
		t.Errorf("Expected Acknowledge(1001) to return true")
	}
	if aq.Len() != 0 {
		t.Errorf("Expected Len() == 0 after ack, got %d", aq.Len())
	}

	// Acknowledging non-existent or duplicate seq should return false
	if aq.Acknowledge(1001) {
		t.Errorf("Expected duplicate Acknowledge(1001) to return false")
	}
	if aq.Acknowledge(9999) {
		t.Errorf("Expected non-existent Acknowledge(9999) to return false")
	}
}

func TestAckQueue_TickRetransmission(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:27015")

	type sentRecord struct {
		addr *net.UDPAddr
		data []byte
	}
	var (
		mu   sync.Mutex
		sent []sentRecord
	)

	sendFn := func(a *net.UDPAddr, d []byte) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, sentRecord{addr: a, data: d})
		return nil
	}

	interval := 100 * time.Millisecond
	aq := NewAckQueue(interval, 3, sendFn)

	startTime := time.Now()
	aq.Enqueue(2001, addr, []byte("retry-data"))

	// Tick before interval should not retransmit
	dropped := aq.Tick(startTime.Add(50 * time.Millisecond))
	if len(dropped) != 0 {
		t.Errorf("Expected 0 dropped packets, got %v", dropped)
	}
	mu.Lock()
	if len(sent) != 0 {
		t.Errorf("Expected 0 retransmissions before interval, got %d", len(sent))
	}
	mu.Unlock()

	// Tick after interval -> Retransmit 1
	tickTime1 := startTime.Add(150 * time.Millisecond)
	dropped = aq.Tick(tickTime1)
	if len(dropped) != 0 {
		t.Errorf("Expected 0 dropped, got %v", dropped)
	}
	mu.Lock()
	if len(sent) != 1 {
		t.Fatalf("Expected 1 retransmission, got %d", len(sent))
	}
	if string(sent[0].data) != "retry-data" {
		t.Errorf("Expected sent data 'retry-data', got %s", string(sent[0].data))
	}
	mu.Unlock()

	entry, found := aq.GetEntry(2001)
	if !found || entry.Retries != 1 {
		t.Errorf("Expected entry Retries=1, got %d (found=%v)", entry.Retries, found)
	}

	// Tick after second interval -> Retransmit 2
	tickTime2 := tickTime1.Add(150 * time.Millisecond)
	dropped = aq.Tick(tickTime2)
	if len(dropped) != 0 {
		t.Errorf("Expected 0 dropped, got %v", dropped)
	}
	mu.Lock()
	if len(sent) != 2 {
		t.Fatalf("Expected 2 retransmissions, got %d", len(sent))
	}
	mu.Unlock()

	entry, _ = aq.GetEntry(2001)
	if entry.Retries != 2 {
		t.Errorf("Expected entry Retries=2, got %d", entry.Retries)
	}

	// Tick after third interval -> Retransmit 3 (Retries reaches MaxRetries = 3)
	tickTime3 := tickTime2.Add(150 * time.Millisecond)
	dropped = aq.Tick(tickTime3)
	if len(dropped) != 0 {
		t.Errorf("Expected 0 dropped, got %v", dropped)
	}
	mu.Lock()
	if len(sent) != 3 {
		t.Fatalf("Expected 3 retransmissions, got %d", len(sent))
	}
	mu.Unlock()

	entry, _ = aq.GetEntry(2001)
	if entry.Retries != 3 {
		t.Errorf("Expected entry Retries=3, got %d", entry.Retries)
	}
}

func TestAckQueue_MaxRetriesExhaustionAndDrop(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:27015")

	var (
		mu        sync.Mutex
		droppedCb []uint32
		dropAddrs []*net.UDPAddr
	)

	aq := NewAckQueue(50*time.Millisecond, 2, nil)
	aq.SetOnDrop(func(seq uint32, a *net.UDPAddr) {
		mu.Lock()
		defer mu.Unlock()
		droppedCb = append(droppedCb, seq)
		dropAddrs = append(dropAddrs, a)
	})

	now := time.Now()
	aq.Enqueue(3001, addr, []byte("drop-test"))

	// Retries start at 0. MaxRetries = 2.
	// Tick 1: interval expired, Retries (0 < 2) -> Retries becomes 1
	dropped := aq.Tick(now.Add(60 * time.Millisecond))
	if len(dropped) != 0 {
		t.Errorf("Tick 1: expected 0 dropped, got %v", dropped)
	}

	// Tick 2: interval expired, Retries (1 < 2) -> Retries becomes 2
	dropped = aq.Tick(now.Add(120 * time.Millisecond))
	if len(dropped) != 0 {
		t.Errorf("Tick 2: expected 0 dropped, got %v", dropped)
	}

	// Tick 3: interval expired, Retries (2 >= 2) -> Drop!
	dropped = aq.Tick(now.Add(180 * time.Millisecond))
	if len(dropped) != 1 || dropped[0] != 3001 {
		t.Fatalf("Tick 3: expected dropped [3001], got %v", dropped)
	}

	if aq.Len() != 0 {
		t.Errorf("Expected queue to be empty after drop, got len=%d", aq.Len())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(droppedCb) != 1 || droppedCb[0] != 3001 {
		t.Errorf("Expected onDrop called for 3001, got %v", droppedCb)
	}
	if len(dropAddrs) != 1 || dropAddrs[0].String() != addr.String() {
		t.Errorf("Expected onDrop addr %v, got %v", addr, dropAddrs)
	}
}

func TestAckQueue_RemoveByAddr(t *testing.T) {
	addr1, _ := net.ResolveUDPAddr("udp", "127.0.0.1:10001")
	addr2, _ := net.ResolveUDPAddr("udp", "127.0.0.1:10002")

	aq := NewAckQueue(500*time.Millisecond, 5, nil)
	aq.Enqueue(101, addr1, []byte("pkt-1"))
	aq.Enqueue(102, addr1, []byte("pkt-2"))
	aq.Enqueue(201, addr2, []byte("pkt-3"))

	if aq.Len() != 3 {
		t.Fatalf("Expected 3 entries, got %d", aq.Len())
	}

	// Remove all addr1 entries
	aq.RemoveByAddr(addr1.String())

	if aq.Len() != 1 {
		t.Fatalf("Expected 1 entry remaining, got %d", aq.Len())
	}

	if _, found := aq.GetEntry(101); found {
		t.Errorf("Entry 101 should have been removed")
	}
	if _, found := aq.GetEntry(102); found {
		t.Errorf("Entry 102 should have been removed")
	}
	if _, found := aq.GetEntry(201); !found {
		t.Errorf("Entry 201 should still exist")
	}

	// Clear should empty the rest
	aq.Clear()
	if aq.Len() != 0 {
		t.Errorf("Expected 0 entries after Clear(), got %d", aq.Len())
	}
}

func TestAckQueue_ConcurrentRace(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:27015")
	aq := NewAckQueue(20*time.Millisecond, 3, func(a *net.UDPAddr, d []byte) error {
		return nil
	})

	var wg sync.WaitGroup
	const iterations = 500

	// Producer 1: Enqueue even sequences
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			seq := uint32(i*2 + 2)
			aq.Enqueue(seq, addr, []byte("even"))
		}
	}()

	// Producer 2: Enqueue odd sequences
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			seq := uint32(i*2 + 1)
			aq.Enqueue(seq, addr, []byte("odd"))
		}
	}()

	// Consumer: Acknowledge sequences randomly
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		for i := 0; i < iterations; i++ {
			seq := uint32(rng.Intn(iterations * 2))
			aq.Acknowledge(seq)
			time.Sleep(10 * time.Microsecond)
		}
	}()

	// Ticker: periodic Tick
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			aq.Tick(time.Now().Add(50 * time.Millisecond))
			time.Sleep(200 * time.Microsecond)
		}
	}()

	// Reader: Len and GetEntry
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = aq.Len()
			_, _ = aq.GetEntry(uint32(i))
			time.Sleep(100 * time.Microsecond)
		}
	}()

	wg.Wait()
}

func TestAckQueue_BackwardsCompatibility(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")

	// 0-argument NewAckQueue
	aq := NewAckQueue()
	if aq == nil {
		t.Fatalf("NewAckQueue() returned nil")
	}

	// EnqueueReliable
	aq.EnqueueReliable(555, addr, []byte{1, 2, 3})
	if aq.Len() != 1 {
		t.Errorf("Expected len=1, got %d", aq.Len())
	}

	// pending map access
	if len(aq.pending) != 1 {
		t.Errorf("Expected len(aq.pending)==1, got %d", len(aq.pending))
	}

	// AckReceived
	aq.AckReceived(555)
	if aq.Len() != 0 {
		t.Errorf("Expected len=0 after AckReceived, got %d", aq.Len())
	}
	if len(aq.pending) != 0 {
		t.Errorf("Expected len(aq.pending)==0 after AckReceived, got %d", len(aq.pending))
	}
}
