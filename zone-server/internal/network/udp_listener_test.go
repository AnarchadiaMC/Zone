package network

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIPRateLimiter_AllowedWithinBurstCapacity(t *testing.T) {
	// Rate = 10 tokens/sec, Capacity = 5 tokens
	limiter := NewIPRateLimiter(10.0, 5.0)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	ip := "192.168.1.50"

	for i := 0; i < 5; i++ {
		if !limiter.Allow(ip, now) {
			t.Fatalf("request %d should be allowed within burst capacity", i+1)
		}
	}
}

func TestIPRateLimiter_RejectionWhenBurstExceeded(t *testing.T) {
	limiter := NewIPRateLimiter(10.0, 5.0)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	ip := "192.168.1.50"

	// Exhaust burst capacity
	for i := 0; i < 5; i++ {
		if !limiter.Allow(ip, now) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// 6th request at the same timestamp should be rejected
	if limiter.Allow(ip, now) {
		t.Fatalf("request 6 should be rejected (burst capacity 5 exceeded)")
	}

	// Further immediate requests should also be rejected
	for i := 0; i < 5; i++ {
		if limiter.Allow(ip, now) {
			t.Fatalf("request %d after burst exceeded should be rejected", i+7)
		}
	}
}

func TestIPRateLimiter_TokenReplenishment(t *testing.T) {
	// Rate: 10 tokens per sec (1 token every 100ms), Capacity: 5 tokens
	limiter := NewIPRateLimiter(10.0, 5.0)
	t0 := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	ip := "192.168.1.60"

	// Exhaust 5 tokens
	for i := 0; i < 5; i++ {
		if !limiter.Allow(ip, t0) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if limiter.Allow(ip, t0) {
		t.Fatal("request 6 should be rejected")
	}

	// Advance 50ms: only 0.5 tokens accumulated, which is < 1.0 -> should be rejected
	t1 := t0.Add(50 * time.Millisecond)
	if limiter.Allow(ip, t1) {
		t.Fatal("request at +50ms should be rejected (< 1 token replenished)")
	}

	// Advance another 50ms (total 100ms from t0): 1 full token accumulated -> should be allowed
	t2 := t0.Add(100 * time.Millisecond)
	if !limiter.Allow(ip, t2) {
		t.Fatal("request at +100ms should be allowed (1 token replenished)")
	}

	// Immediately subsequent request should be rejected (token consumed)
	if limiter.Allow(ip, t2) {
		t.Fatal("immediate second request at +100ms should be rejected")
	}

	// Advance 2 seconds: should replenish up to capacity (capped at 5, not 20)
	t3 := t2.Add(2 * time.Second)
	for i := 0; i < 5; i++ {
		if !limiter.Allow(ip, t3) {
			t.Fatalf("request %d after 2s replenishment should be allowed", i+1)
		}
	}
	// 6th should be rejected (verifying tokens did not exceed capacity 5)
	if limiter.Allow(ip, t3) {
		t.Fatal("request 6 after replenishment should be rejected (capacity capped at 5)")
	}
}

func TestIPRateLimiter_DistinctIPs(t *testing.T) {
	limiter := NewIPRateLimiter(5.0, 2.0)
	now := time.Now()

	ip1 := "10.0.0.1"
	ip2 := "10.0.0.2"
	ip3 := "10.0.0.3"

	// IP1 uses both burst tokens
	if !limiter.Allow(ip1, now) || !limiter.Allow(ip1, now) {
		t.Fatal("ip1 requests should be allowed")
	}
	if limiter.Allow(ip1, now) {
		t.Fatal("ip1 3rd request should be rejected")
	}

	// IP2 should NOT be affected by IP1
	if !limiter.Allow(ip2, now) || !limiter.Allow(ip2, now) {
		t.Fatal("ip2 requests should be allowed independently")
	}
	if limiter.Allow(ip2, now) {
		t.Fatal("ip2 3rd request should be rejected")
	}

	// IP3 should also have full capacity
	if !limiter.Allow(ip3, now) {
		t.Fatal("ip3 1st request should be allowed")
	}

	// IP1 should still be exhausted
	if limiter.Allow(ip1, now) {
		t.Fatal("ip1 should still be exhausted")
	}
}

func TestIPRateLimiter_StaleEntryCleanup(t *testing.T) {
	limiter := NewIPRateLimiter(10.0, 10.0)
	t0 := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	// IP1 activity at t0
	limiter.Allow("1.1.1.1", t0)
	// IP2 activity at t0 + 20s
	limiter.Allow("2.2.2.2", t0.Add(20*time.Second))

	limiter.mu.Lock()
	if len(limiter.buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(limiter.buckets))
	}
	limiter.mu.Unlock()

	// At t0 + 65s, IP3 sends a packet
	// Elapsed since lastClean (t0) is 65s >= 60s -> cleanup triggers!
	// IP1 last updated at t0: 65s - 0s = 65s > 60s -> STALE (cleaned up)
	// IP2 last updated at t0 + 20s: 65s - 20s = 45s <= 60s -> ACTIVE (kept)
	tCleanup := t0.Add(65 * time.Second)
	limiter.Allow("3.3.3.3", tCleanup)

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if _, exists := limiter.buckets["1.1.1.1"]; exists {
		t.Errorf("expected 1.1.1.1 to be cleaned up as stale (>60s idle)")
	}
	if _, exists := limiter.buckets["2.2.2.2"]; !exists {
		t.Errorf("expected 2.2.2.2 to remain in buckets (idle 45s <= 60s)")
	}
	if _, exists := limiter.buckets["3.3.3.3"]; !exists {
		t.Errorf("expected 3.3.3.3 to exist in buckets")
	}
}

func TestIPRateLimiter_SetRateLimit(t *testing.T) {
	limiter := NewIPRateLimiter(1.0, 2.0)
	now := time.Now()
	ip := "172.16.0.1"

	if !limiter.Allow(ip, now) || !limiter.Allow(ip, now) {
		t.Fatal("first 2 requests should be allowed")
	}
	if limiter.Allow(ip, now) {
		t.Fatal("3rd request should be rejected")
	}

	// Update rate limit to high capacity
	limiter.SetRateLimit(100.0, 10.0)

	// After 100ms with rate=100, 10 tokens replenish
	now2 := now.Add(100 * time.Millisecond)
	allowedCount := 0
	for i := 0; i < 15; i++ {
		if limiter.Allow(ip, now2) {
			allowedCount++
		}
	}
	if allowedCount != 10 {
		t.Fatalf("expected 10 allowed requests with new capacity, got %d", allowedCount)
	}
}

func TestIPRateLimiter_ConcurrentRace(t *testing.T) {
	limiter := NewIPRateLimiter(100.0, 50.0)
	var wg sync.WaitGroup

	ips := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3", "10.0.0.1", "10.0.0.2"}

	// 20 reader goroutines
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			ip := ips[workerID%len(ips)]
			for j := 0; j < 100; j++ {
				limiter.Allow(ip, time.Now())
			}
		}(i)
	}

	// Writer goroutine adjusting rate limit concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			limiter.SetRateLimit(float64(50+j), float64(20+j))
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Cleaner goroutine running cleanup concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			limiter.Cleanup(time.Now())
			time.Sleep(1 * time.Millisecond)
		}
	}()

	wg.Wait()
}

func TestUDPListener_Integration_FloodDrop(t *testing.T) {
	var receivedCount atomic.Int32
	receivedPackets := make(chan []byte, 100)

	listener, err := NewUDPListener(0, func(data []byte, addr *net.UDPAddr) {
		receivedCount.Add(1)
		select {
		case receivedPackets <- data:
		default:
		}
	})
	if err != nil {
		t.Fatalf("failed to create UDPListener: %v", err)
	}
	defer listener.Close()

	// Set low burst capacity: 5 tokens, rate 10 tokens/sec
	listener.SetRateLimit(10.0, 5.0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener.Start(ctx)

	serverAddr := listener.Addr()
	if serverAddr == nil {
		t.Fatal("expected non-nil server address")
	}

	clientConn, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		t.Fatalf("failed to dial UDP listener: %v", err)
	}
	defer clientConn.Close()

	// Send flood of 30 packets from this client
	totalSent := 30
	for i := 0; i < totalSent; i++ {
		_, err := clientConn.Write([]byte("flood-packet"))
		if err != nil {
			t.Fatalf("failed to write packet %d: %v", i, err)
		}
	}

	// Give the listener time to read and process packets
	time.Sleep(100 * time.Millisecond)

	handled := int(receivedCount.Load())
	dropped := int(listener.DroppedPackets())

	t.Logf("Total sent: %d, Handled: %d, Dropped: %d", totalSent, handled, dropped)

	// Burst capacity is 5. Within the tiny loop, at most 5-6 packets could be allowed.
	if handled > 7 {
		t.Errorf("expected at most ~5-6 packets allowed by rate limiter, but %d were handled", handled)
	}

	// At least 20 packets should have been dropped due to rate limiting
	if dropped < 20 {
		t.Errorf("expected at least 20 packets dropped by rate limiter, got %d", dropped)
	}

	if handled+dropped != totalSent {
		t.Errorf("expected handled (%d) + dropped (%d) == totalSent (%d)", handled, dropped, totalSent)
	}
}
