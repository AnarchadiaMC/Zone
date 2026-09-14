package network

import (
	"context"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type incomingPacket struct {
	data []byte
	addr *net.UDPAddr
}

type tokenBucket struct {
	tokens      float64
	lastUpdated time.Time
}

type IPRateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	rate      float64 // tokens per second (e.g. 100.0)
	capacity  float64 // max burst capacity (e.g. 150.0)
	lastClean time.Time
}

func NewIPRateLimiter(rate, capacity float64) *IPRateLimiter {
	return &IPRateLimiter{
		buckets:   make(map[string]*tokenBucket),
		rate:      rate,
		capacity:  capacity,
		lastClean: time.Now(),
	}
}

func (r *IPRateLimiter) Allow(ip string, now time.Time) bool {
	if r == nil {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Periodically clean up stale IP entries (> 60s idle) to prevent memory leak.
	if r.lastClean.IsZero() || now.Before(r.lastClean) {
		r.lastClean = now
	} else if now.Sub(r.lastClean) >= 60*time.Second {
		r.cleanup(now)
	}

	b, exists := r.buckets[ip]
	if !exists {
		b = &tokenBucket{
			tokens:      r.capacity,
			lastUpdated: now,
		}
		r.buckets[ip] = b
	} else {
		elapsed := now.Sub(b.lastUpdated).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * r.rate
			if b.tokens > r.capacity {
				b.tokens = r.capacity
			}
			b.lastUpdated = now
		}
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}

	return false
}

func (r *IPRateLimiter) cleanup(now time.Time) {
	r.lastClean = now
	for ip, b := range r.buckets {
		if now.Sub(b.lastUpdated) > 60*time.Second {
			delete(r.buckets, ip)
		}
	}
}

func (r *IPRateLimiter) Cleanup(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanup(now)
}

func (r *IPRateLimiter) SetRateLimit(rate, capacity float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rate = rate
	r.capacity = capacity
}

type UDPListener struct {
	conn           *net.UDPConn
	pool           sync.Pool
	workers        [8]chan *incomingPacket
	handler        func(data []byte, addr *net.UDPAddr)
	droppedPackets atomic.Uint64
	limiter        *IPRateLimiter
}

func (l *UDPListener) DroppedPackets() uint64 {
	return l.droppedPackets.Load()
}

func (l *UDPListener) Limiter() *IPRateLimiter {
	return l.limiter
}

func (l *UDPListener) SetRateLimit(rate, capacity float64) {
	if l.limiter != nil {
		l.limiter.SetRateLimit(rate, capacity)
	}
}

func (l *UDPListener) Addr() *net.UDPAddr {
	if l.conn != nil {
		if a, ok := l.conn.LocalAddr().(*net.UDPAddr); ok {
			return a
		}
	}
	return nil
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
		limiter: NewIPRateLimiter(100.0, 150.0),
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

	go l.listen()
}

func (l *UDPListener) listen() {
	for {
		bufPtr := l.pool.Get().(*[]byte)
		buf := *bufPtr
		n, remoteAddr, err := l.conn.ReadFromUDP(buf)
		if err != nil {
			l.pool.Put(bufPtr)
			return
		}

		if l.limiter != nil && remoteAddr != nil && remoteAddr.IP != nil {
			remoteIP := remoteAddr.IP.String()
			if !l.limiter.Allow(remoteIP, time.Now()) {
				l.droppedPackets.Add(1)
				l.pool.Put(bufPtr)
				continue
			}
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		l.pool.Put(bufPtr)

		var hash uint32 = 2166136261
		for _, c := range remoteAddr.IP {
			hash ^= uint32(c)
			hash *= 16777619
		}
		hash ^= uint32(remoteAddr.Port & 0xff)
		hash *= 16777619
		hash ^= uint32((remoteAddr.Port >> 8) & 0xff)
		hash *= 16777619
		workerIdx := hash % 8

		select {
		case l.workers[workerIdx] <- &incomingPacket{data: data, addr: remoteAddr}:
		default:
			dropped := l.droppedPackets.Add(1)
			if dropped == 1 || dropped%100 == 0 {
				log.Printf("[WARN] UDP listener: worker queue full, packet dropped (total dropped: %d)", dropped)
			}
		}
	}
}

func (l *UDPListener) Send(addr *net.UDPAddr, data []byte) error {
	_, err := l.conn.WriteToUDP(data, addr)
	return err
}

func (l *UDPListener) Close() error {
	return l.conn.Close()
}
