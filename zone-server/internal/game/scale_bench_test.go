package game

import (
	"fmt"
	"math"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/network"
)

// scaleBenchServer builds a Server with count sessions placed on a ring of the
// given radius (XZ plane) and a real UDP listener so the full tick path -
// including snapshot serialisation and socket sends - is exercised.
func scaleBenchServer(b *testing.B, count int, ringRadius float32) (*Server, func()) {
	b.Helper()

	s := NewServer(&config.Config{TickRateHz: 30, MaxPlayers: count}, nil, zap.NewNop())
	activeSink = nil

	udp, err := network.NewUDPListener(0, func([]byte, *net.UDPAddr) {})
	if err != nil {
		b.Fatalf("NewUDPListener: %v", err)
	}
	s.udp = udp

	for i := 0; i < count; i++ {
		angle := 2 * math.Pi * float64(i) / float64(count)
		x := float32(math.Cos(angle)) * ringRadius
		z := float32(math.Sin(angle)) * ringRadius
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30000 + i}
		sess := &network.PlayerSession{
			SessionID:      uint32(i + 1),
			AccountID:      fmt.Sprintf("bench-%d", i),
			Name:           fmt.Sprintf("Bench%d", i),
			UDPAddr:        addr,
			CurrentLevel:   "l01_escape",
			Position:       [3]float32{x, 0, z},
			Health:         100,
			LastSeen:       time.Now().Add(time.Hour),
			LastCheckpoint: time.Now().Add(time.Hour),
		}
		s.sessions.AddSession(sess)
		s.grid.Insert(sess.SessionID, x, z)
	}

	return s, func() { _ = udp.Close() }
}

// BenchmarkTick48Dense runs the full 30Hz tick with 48 sessions packed inside
// one AoI (ring radius 100, pairwise distance <= 200 < AoIRadius 220), so every
// session receives a chunked snapshot broadcast every tick.
func BenchmarkTick48Dense(b *testing.B) {
	s, cleanup := scaleBenchServer(b, 48, 100)
	defer cleanup()

	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Tick(now)
	}
}

// BenchmarkTick48Spread runs the full tick with 48 sessions spread over a
// wider ring where most AoI queries return a handful of neighbours.
func BenchmarkTick48Spread(b *testing.B) {
	s, cleanup := scaleBenchServer(b, 48, 500)
	defer cleanup()

	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Tick(now)
	}
}

// BenchmarkAoIBroadcast48 isolates snapshot generation + broadcast for 48
// sessions inside one AoI.
func BenchmarkAoIBroadcast48(b *testing.B) {
	s, cleanup := scaleBenchServer(b, 48, 100)
	defer cleanup()

	udp := s.GetUDP()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.aoi.BroadcastSnapshots(s.sessions, s.grid, udp, &s.seq)
	}
}

// BenchmarkSpatialGridGetNeighbors48Dense measures 48 AoI-radius queries over
// a dense 48-entity cluster.
func BenchmarkSpatialGridGetNeighbors48Dense(b *testing.B) {
	grid := NewSpatialGrid(64.0)
	for i := 0; i < 48; i++ {
		angle := 2 * math.Pi * float64(i) / 48
		grid.Insert(uint32(i+1), float32(math.Cos(angle))*100, float32(math.Sin(angle))*100)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 48; j++ {
			angle := 2 * math.Pi * float64(j) / 48
			_ = grid.GetNeighbors(float32(math.Cos(angle))*100, float32(math.Sin(angle))*100, AoIRadius)
		}
	}
}

// BenchmarkSpatialGridGetNeighborsParallel measures read-lock contention for
// concurrent AoI queries from many goroutines over the same dense grid.
func BenchmarkSpatialGridGetNeighborsParallel(b *testing.B) {
	grid := NewSpatialGrid(64.0)
	for i := 0; i < 48; i++ {
		angle := 2 * math.Pi * float64(i) / 48
		grid.Insert(uint32(i+1), float32(math.Cos(angle))*100, float32(math.Sin(angle))*100)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			angle := 2 * math.Pi * float64(i%48) / 48
			_ = grid.GetNeighbors(float32(math.Cos(angle))*100, float32(math.Sin(angle))*100, AoIRadius)
			i++
		}
	})
}

// discardSender drops packets, isolating snapshot build + serialisation cost
// from socket writes.
type discardSender struct{}

func (discardSender) Send(*net.UDPAddr, []byte) error { return nil }

// BenchmarkAoIBroadcast48_NoSend measures the CPU/allocation cost of building
// and serialising 48 chunked snapshot broadcasts without UDP syscalls.
func BenchmarkAoIBroadcast48_NoSend(b *testing.B) {
	s, cleanup := scaleBenchServer(b, 48, 100)
	defer cleanup()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.aoi.BroadcastSnapshots(s.sessions, s.grid, discardSender{}, &s.seq)
	}
}
