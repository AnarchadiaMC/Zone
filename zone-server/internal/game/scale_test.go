package game

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// captureSender records every packet per destination address so tests can
// inspect exactly what each client was sent.
type captureSender struct {
	mu      sync.Mutex
	packets map[string][][]byte
}

func newCaptureSender() *captureSender {
	return &captureSender{packets: make(map[string][][]byte)}
}

func (c *captureSender) Send(addr *net.UDPAddr, data []byte) error {
	b := make([]byte, len(data))
	copy(b, data)
	c.mu.Lock()
	c.packets[addr.String()] = append(c.packets[addr.String()], b)
	c.mu.Unlock()
	return nil
}

func (c *captureSender) forAddr(addr *net.UDPAddr) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.packets[addr.String()]))
	copy(out, c.packets[addr.String()])
	return out
}

// scaleTestServer builds a database-free server with count sessions evenly
// spaced on a ring of ringRadius in the XZ plane.
func scaleTestServer(t *testing.T, count int, ringRadius float32) (*Server, []*network.PlayerSession) {
	t.Helper()

	s := NewServer(&config.Config{TickRateHz: 30, MaxPlayers: count}, nil, zap.NewNop())
	activeSink = nil

	sessions := make([]*network.PlayerSession, 0, count)
	for i := 0; i < count; i++ {
		angle := 2 * math.Pi * float64(i) / float64(count)
		x := float32(math.Cos(angle)) * ringRadius
		z := float32(math.Sin(angle)) * ringRadius
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31000 + i}
		sess := &network.PlayerSession{
			SessionID:      uint32(i + 1),
			AccountID:      fmt.Sprintf("scale-%d", i),
			Name:           fmt.Sprintf("Scale%d", i),
			UDPAddr:        addr,
			CurrentLevel:   "l01_escape",
			Position:       [3]float32{x, 0, z},
			Health:         100,
			LastSeen:       time.Now(),
			LastCheckpoint: time.Now(),
		}
		s.sessions.AddSession(sess)
		s.grid.Insert(sess.SessionID, x, z)
		sessions = append(sessions, sess)
	}
	return s, sessions
}

func sessionPos(s *Server, id uint32) (float32, float32) {
	sess := s.sessions.GetByID(id)
	if sess == nil {
		return 0, 0
	}
	sess.Lock()
	defer sess.Unlock()
	return sess.Position[0], sess.Position[2]
}

// With 47 neighbours in one AoI, every client must receive ceil(47/32)=2
// snapshot packets and the union of their entries must contain all 47 peers
// exactly once, ordered nearest-first across the chunk sequence.
func TestSnapshotChunking_AllNeighborsDeliveredExactlyOnce(t *testing.T) {
	const n = 48
	s, sessions := scaleTestServer(t, n, 100)

	sender := newCaptureSender()
	var seq atomic.Uint32
	s.aoi.BroadcastSnapshots(s.sessions, s.grid, sender, &seq)

	for _, self := range sessions {
		packets := sender.forAddr(self.UDPAddr)
		if len(packets) < 2 {
			t.Fatalf("session %d: got %d snapshot packets, want >= 2 for 47 neighbours",
				self.SessionID, len(packets))
		}

		seen := make(map[uint32]bool, n-1)
		var prevDistSq float32 = -1
		total := 0
		sawFullChunk := false

		selfX, selfZ := sessionPos(s, self.SessionID)

		for pi, raw := range packets {
			r := bytes.NewReader(raw)
			hdr, err := protocol.ReadHeader(r)
			if err != nil {
				t.Fatalf("session %d packet %d: ReadHeader: %v", self.SessionID, pi, err)
			}
			if hdr.Opcode != protocol.OpServerSnapshot {
				t.Fatalf("session %d packet %d: opcode 0x%04X, want 0x%04X",
					self.SessionID, pi, hdr.Opcode, protocol.OpServerSnapshot)
			}
			rawSnap, err := protocol.ReadServerSnapshot(r)
			if err != nil {
				t.Fatalf("session %d packet %d: ReadServerSnapshot: %v", self.SessionID, pi, err)
			}
			if rawSnap.Count == 0 || int(rawSnap.Count) > protocol.MaxSnapshotEntries {
				t.Fatalf("session %d packet %d: count %d out of wire range 1..%d",
					self.SessionID, pi, rawSnap.Count, protocol.MaxSnapshotEntries)
			}
			if int(rawSnap.Count) == protocol.MaxSnapshotEntries {
				sawFullChunk = true
			}

			for i := 0; i < int(rawSnap.Count); i++ {
				e := rawSnap.Entries[i]
				if e.SessionID == self.SessionID {
					t.Fatalf("session %d packet %d entry %d: self included", self.SessionID, pi, i)
				}
				if seen[e.SessionID] {
					t.Fatalf("session %d: peer %d delivered more than once", self.SessionID, e.SessionID)
				}
				seen[e.SessionID] = true
				total++

				ex, ez := sessionPos(s, e.SessionID)
				dx, dz := ex-selfX, ez-selfZ
				distSq := dx*dx + dz*dz
				if distSq < prevDistSq {
					t.Fatalf("session %d: snapshot order not nearest-first: peer %d distSq %f after %f",
						self.SessionID, e.SessionID, distSq, prevDistSq)
				}
				prevDistSq = distSq

				if e.PosX != ex || e.PosZ != ez {
					t.Fatalf("session %d: peer %d position mismatch: got (%f,%f) want (%f,%f)",
						self.SessionID, e.SessionID, e.PosX, e.PosZ, ex, ez)
				}
			}
		}

		if !sawFullChunk {
			t.Fatalf("session %d: no packet carried the full %d-entry chunk", self.SessionID, protocol.MaxSnapshotEntries)
		}
		if total != n-1 {
			t.Fatalf("session %d: delivered %d entries across chunks, want %d", self.SessionID, total, n-1)
		}
		for _, peer := range sessions {
			if peer.SessionID == self.SessionID {
				continue
			}
			if !seen[peer.SessionID] {
				t.Fatalf("session %d: peer %d was silently dropped", self.SessionID, peer.SessionID)
			}
		}
	}
}

// 30 ticks (one simulated second) with 48 dense sessions must finish well
// inside the tick budget and leave all sessions/grid entries intact.
func TestTick48SessionsSmoke_NoDeadlock(t *testing.T) {
	const n = 48
	s, _ := scaleTestServer(t, n, 100)

	udp, err := network.NewUDPListener(0, func([]byte, *net.UDPAddr) {})
	if err != nil {
		t.Fatalf("NewUDPListener: %v", err)
	}
	defer udp.Close()
	s.udp = udp
	activeSink = nil

	now := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 30; i++ {
			s.Tick(now)
		}
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("deadlock: 30 ticks with 48 sessions did not finish in 15s")
	}

	if got := s.sessions.Count(); got != n {
		t.Fatalf("after 30 ticks: %d sessions, want %d", got, n)
	}
	if got := s.grid.Count(); got != n {
		t.Fatalf("after 30 ticks: %d grid entities, want %d", got, n)
	}
}

// readLastHandshakeStatus scans the sink backwards for the last HandshakeRes.
func readLastHandshakeStatus(t *testing.T, sink *fakeSink) uint8 {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for i := len(sink.sent) - 1; i >= 0; i-- {
		r := bytes.NewReader(sink.sent[i])
		hdr, err := protocol.ReadHeader(r)
		if err != nil || hdr.Opcode != protocol.OpHandshakeRes {
			continue
		}
		var res protocol.HandshakeRes
		if err := binary.Read(r, binary.LittleEndian, &res); err != nil {
			t.Fatalf("read HandshakeRes: %v", err)
		}
		return res.Status
	}
	t.Fatalf("no HandshakeRes found in %d packets", len(sink.sent))
	return 255
}

// The 49th (max 48) and 65th (max 64) distinct player must be rejected with
// Status 1 (full) and must not be added; the cap must still hold exactly.
func TestScale_MaxPlayersFull(t *testing.T) {
	for _, max := range []int{48, 64} {
		max := max
		t.Run(fmt.Sprintf("max_%d", max), func(t *testing.T) {
			s := NewServer(&config.Config{MaxPlayers: max}, nil, zap.NewNop())
			sink := &fakeSink{}
			s.udp = sink.UDPListener()
			defer s.GetUDP().Close()

			handshake := func(i int) (*net.UDPAddr, []byte) {
				addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 51000 + i}
				var req protocol.HandshakeReq
				copy(req.UUID[:], fmt.Sprintf("uuid-max%d-%d", max, i))
				copy(req.Nickname[:], fmt.Sprintf("P%d_%d", max, i))
				req.ProtocolVer = protocol.ProtocolVer
				return addr, buildTestPacket(t, protocol.OpHandshakeReq, uint32(i+1), protocol.FlagReliable, req)
			}

			for i := 0; i < max; i++ {
				addr, raw := handshake(i)
				s.HandlePacket(raw, addr)
				if s.sessions.GetByAddr(addr.String()) == nil {
					t.Fatalf("player %d rejected under cap %d", i, max)
				}
			}
			if got := s.sessions.Count(); got != max {
				t.Fatalf("session count %d at cap %d", got, max)
			}

			overflowAddr, overflowRaw := handshake(max)
			sink.Reset()
			s.HandlePacket(overflowRaw, overflowAddr)
			if s.sessions.GetByAddr(overflowAddr.String()) != nil {
				t.Fatalf("overflow player must not be added at cap %d", max)
			}
			if st := readLastHandshakeStatus(t, sink); st != 1 {
				t.Fatalf("overflow player status %d, want 1 (full)", st)
			}
			if got := s.sessions.Count(); got != max {
				t.Fatalf("cap %d no longer exact after rejection: %d", max, got)
			}
		})
	}
}
