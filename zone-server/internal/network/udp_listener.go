package network

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"
)

type incomingPacket struct {
	data []byte
	addr *net.UDPAddr
}

type AckEntry struct {
	data   []byte
	addr   *net.UDPAddr
	sentAt time.Time
	seq    uint32
}

type AckQueue struct {
	mu      sync.Mutex
	pending map[uint32]*AckEntry
}

func NewAckQueue() *AckQueue {
	return &AckQueue{
		pending: make(map[uint32]*AckEntry),
	}
}

func (aq *AckQueue) EnqueueReliable(seq uint32, addr *net.UDPAddr, data []byte) {
	aq.mu.Lock()
	defer aq.mu.Unlock()
	b := make([]byte, len(data))
	copy(b, data)
	aq.pending[seq] = &AckEntry{
		data:   b,
		addr:   addr,
		sentAt: time.Now(),
		seq:    seq,
	}
}

func (aq *AckQueue) AckReceived(seq uint32) {
	aq.mu.Lock()
	defer aq.mu.Unlock()
	delete(aq.pending, seq)
}

func (aq *AckQueue) RetransmitExpired(now time.Time, listener *UDPListener) {
	aq.mu.Lock()
	defer aq.mu.Unlock()
	for _, entry := range aq.pending {
		if now.Sub(entry.sentAt) > 500*time.Millisecond {
			_ = listener.Send(entry.addr, entry.data)
			entry.sentAt = now
		}
	}
}

type UDPListener struct {
	conn    *net.UDPConn
	pool    sync.Pool
	workers [8]chan *incomingPacket
	handler func(data []byte, addr *net.UDPAddr)
}

func NewUDPListener(port int, handler func([]byte, *net.UDPAddr)) (*UDPListener, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}

	listener := &UDPListener{
		conn:    conn,
		handler: handler,
	}
	listener.pool.New = func() interface{} {
		b := make([]byte, 1500)
		return &b
	}
	for i := 0; i < 8; i++ {
		listener.workers[i] = make(chan *incomingPacket, 1024)
	}
	return listener, nil
}

func (l *UDPListener) Start(ctx context.Context) {
	for i := 0; i < 8; i++ {
		go func(ch chan *incomingPacket) {
			for {
				select {
				case <-ctx.Done():
					return
				case pkt := <-ch:
					if l.handler != nil {
						l.handler(pkt.data, pkt.addr)
					}
				}
			}
		}(l.workers[i])
	}

	go func() {
		for {
			bufPtr := l.pool.Get().(*[]byte)
			buf := *bufPtr
			n, addr, err := l.conn.ReadFromUDP(buf)
			if err != nil {
				return
			}

			data := make([]byte, n)
			copy(data, buf[:n])
			l.pool.Put(bufPtr)

			var hash uint32 = 2166136261
			for _, c := range addr.IP {
				hash ^= uint32(c)
				hash *= 16777619
			}
			hash ^= uint32(addr.Port & 0xff)
			hash *= 16777619
			hash ^= uint32((addr.Port >> 8) & 0xff)
			hash *= 16777619
			workerIdx := hash % 8

			select {
			case l.workers[workerIdx] <- &incomingPacket{data: data, addr: addr}:
			default:
			}
		}
	}()
}

func (l *UDPListener) Send(addr *net.UDPAddr, data []byte) error {
	_, err := l.conn.WriteToUDP(data, addr)
	return err
}

func (l *UDPListener) Close() error {
	return l.conn.Close()
}
