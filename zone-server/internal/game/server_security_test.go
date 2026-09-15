package game

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// countOpcodePackets counts recorded sink packets with the given opcode.
// Required because reliable inbound also records an OpAck; broadcast
// assertions must filter by opcode, not total packet count.
func countOpcodePackets(sink *fakeSink, op uint16) int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	n := 0
	for _, data := range sink.sent {
		if len(data) < 12 {
			continue
		}
		hdr, err := protocol.ReadHeader(bytes.NewReader(data[:12]))
		if err != nil {
			continue
		}
		if hdr.Opcode == op {
			n++
		}
	}
	return n
}

func sendChatFrom(t *testing.T, s *Server, addr *net.UDPAddr, seq uint32, senderID uint32, text string) {
	t.Helper()
	pkt := protocol.NewChatText(senderID, text)
	raw := buildTestPacket(t, protocol.OpChatText, seq, protocol.FlagReliable, pkt)
	s.HandlePacket(raw, addr)
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Stale reliable replay dropped
// ─────────────────────────────────────────────────────────────────────────────

func TestSecurity_ReliableReplayDropped(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41001}
	sess := &network.PlayerSession{
		SessionID:    7001,
		AccountID:    "uuid-replay-1",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)
	sink.Reset()

	// Fresh chat seq 20 → delivered (1 broadcast).
	sendChatFrom(t, s, addr, 20, 7001, "first")
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 1 {
		t.Fatalf("expected 1 chat broadcast after seq 20, got %d", got)
	}

	// Exact replay seq 20 → dropped (no new broadcast; ACK still sent).
	sendChatFrom(t, s, addr, 20, 7001, "first")
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 1 {
		t.Fatalf("replay seq 20 was not dropped: chat count = %d, want 1", got)
	}

	// Older seq 19 → dropped.
	sendChatFrom(t, s, addr, 19, 7001, "old")
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 1 {
		t.Fatalf("stale seq 19 was not dropped: chat count = %d, want 1", got)
	}

	// Newer seq 21 → delivered.
	sendChatFrom(t, s, addr, 21, 7001, "second")
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 2 {
		t.Fatalf("expected 2 chat broadcasts after seq 21, got %d", got)
	}
}

// Stale unreliable (transform) dropped.
func TestSecurity_StaleUnreliableDropped(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41002}
	sess := &network.PlayerSession{
		SessionID:    7002,
		AccountID:    "uuid-replay-2",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{0, 0, 0},
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)

	// Seed LastSequence via a valid nearby transform with high seq.
	ct1 := protocol.ClientTransform{PosX: 1, PosY: 0, PosZ: 1}
	binary.LittleEndian.PutUint32(ct1.SessionID[:], 7002)
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 30, protocol.FlagUnreliable, ct1), addr)

	sess.Lock()
	posAfter := sess.Position
	lastSeq := sess.LastSequence
	sess.Unlock()
	if lastSeq != 30 {
		t.Fatalf("expected LastSequence 30 after fresh transform, got %d", lastSeq)
	}
	if posAfter[0] != 1 {
		t.Fatalf("expected position x=1 after fresh transform, got %v", posAfter)
	}

	// Stale transform seq 29 → dropped, position unchanged.
	ctStale := protocol.ClientTransform{PosX: 2, PosY: 0, PosZ: 2}
	binary.LittleEndian.PutUint32(ctStale.SessionID[:], 7002)
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 29, protocol.FlagUnreliable, ctStale), addr)

	sess.Lock()
	posStale := sess.Position
	sess.Unlock()
	if posStale[0] != 1 {
		t.Fatalf("stale unreliable transform was not dropped: pos = %v, want x=1", posStale)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Second connect over cap rejected (+ same-addr replace allowed)
// ─────────────────────────────────────────────────────────────────────────────

func TestSecurity_MaxPlayersCapped(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	s.cfg = &config.Config{MaxPlayers: 1}

	newHS := func(uuid string, seq uint32) []byte {
		var req protocol.HandshakeReq
		copy(req.UUID[:], uuid)
		copy(req.Nickname[:], "Stalker")
		req.ProtocolVer = protocol.ProtocolVer
		return buildTestPacket(t, protocol.OpHandshakeReq, seq, protocol.FlagReliable, req)
	}
	readStatus := func() uint8 {
		// Last packet may be the ACK; scan back for HandshakeRes.
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for i := len(sink.sent) - 1; i >= 0; i-- {
			rr := bytes.NewReader(sink.sent[i])
			hdr, err := protocol.ReadHeader(rr)
			if err != nil || hdr.Opcode != protocol.OpHandshakeRes {
				continue
			}
			var res protocol.HandshakeRes
			if err := binary.Read(rr, binary.LittleEndian, &res); err != nil {
				t.Fatalf("failed to read HandshakeRes: %v", err)
			}
			return res.Status
		}
		t.Fatalf("no HandshakeRes found in %d packets", len(sink.sent))
		return 255
	}

	addr1 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41011}
	sink.Reset()
	s.HandlePacket(newHS("uuid-cap-first", 1), addr1)
	if s.sessions.GetByAddr(addr1.String()) == nil {
		t.Fatalf("first player should connect under cap")
	}
	if st := readStatus(); st != 0 {
		t.Fatalf("expected first handshake Status 0, got %d", st)
	}

	// Second distinct addr over cap → rejected Status 1, not added.
	addr2 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41012}
	sink.Reset()
	s.HandlePacket(newHS("uuid-cap-second", 2), addr2)
	if s.sessions.GetByAddr(addr2.String()) != nil {
		t.Fatalf("second player should NOT be added when server is full")
	}
	if st := readStatus(); st != 1 {
		t.Fatalf("expected second handshake Status 1 (full), got %d", st)
	}
	if got := s.sessions.Count(); got != 1 {
		t.Fatalf("expected session count 1 at cap, got %d", got)
	}

	// Same-addr reconnect (newer seq) replaces instead of counting twice.
	sink.Reset()
	s.HandlePacket(newHS("uuid-cap-first", 2), addr1)
	if got := s.sessions.Count(); got != 1 {
		t.Fatalf("same-addr reconnect should replace, not count twice: count = %d, want 1", got)
	}
	if s.sessions.GetByAddr(addr1.String()) == nil {
		t.Fatalf("reconnected same-addr session should exist")
	}
	if st := readStatus(); st != 0 {
		t.Fatalf("expected reconnect handshake Status 0, got %d", st)
	}
}

// Concurrent TryAddCapped never exceeds cap (TOCTOU regression).
func TestSecurity_TryAddCappedConcurrent(t *testing.T) {
	sm := network.NewSessionManager()
	const max = 5
	const racers = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	added := 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:42000")
			// Distinct port per racer so they are distinct addrs.
			addr.Port = 42000 + i
			ok := sm.TryAddCapped(&network.PlayerSession{
				SessionID: uint32(8000 + i),
				AccountID: "uuid-race",
				UDPAddr:   addr,
				LastSeen:  time.Now(),
			}, max)
			if ok {
				mu.Lock()
				added++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if added != max {
		t.Fatalf("expected exactly %d successful adds under concurrency, got %d", max, added)
	}
	if got := sm.Count(); got != max {
		t.Fatalf("expected manager count %d, got %d", max, got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Cross-level chat not delivered (+ forgery + rate limit)
// ─────────────────────────────────────────────────────────────────────────────

func TestSecurity_ChatCrossLevelNotDelivered(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	addr1 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41021}
	addr2 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41022}
	s.sessions.AddSession(&network.PlayerSession{
		SessionID: 9001, AccountID: "user-a", UDPAddr: addr1,
		CurrentLevel: "l01_escape", LastSeen: time.Now(),
	})
	s.sessions.AddSession(&network.PlayerSession{
		SessionID: 9002, AccountID: "user-b", UDPAddr: addr2,
		CurrentLevel: "l02_garbage", LastSeen: time.Now(),
	})
	sink.Reset()

	sendChatFrom(t, s, addr1, 40, 9001, "hello level 1")
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 1 {
		t.Fatalf("cross-level chat leaked: got %d chat broadcasts, want 1 (sender level only)", got)
	}
}

func TestSecurity_ChatForgedSenderRejected(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41031}
	s.sessions.AddSession(&network.PlayerSession{
		SessionID: 9101, AccountID: "user-c", UDPAddr: addr,
		CurrentLevel: "l01_escape", LastSeen: time.Now(),
	})
	sink.Reset()

	// SenderID == 0 must be rejected.
	sendChatFrom(t, s, addr, 50, 0, "forged zero")
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 0 {
		t.Fatalf("SenderID 0 was not rejected: got %d broadcasts, want 0", got)
	}

	// Mismatched SenderID must be rejected.
	sendChatFrom(t, s, addr, 51, 9999, "forged other")
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 0 {
		t.Fatalf("mismatched SenderID was not rejected: got %d broadcasts, want 0", got)
	}

	// Correct SenderID passes.
	sendChatFrom(t, s, addr, 52, 9101, "legit")
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 1 {
		t.Fatalf("legit chat was dropped: got %d broadcasts, want 1", got)
	}
}

func TestSecurity_ChatRateLimit(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41041}
	s.sessions.AddSession(&network.PlayerSession{
		SessionID: 9201, AccountID: "user-d", UDPAddr: addr,
		CurrentLevel: "l01_escape", LastSeen: time.Now(),
	})
	sink.Reset()

	// 6 rapid messages with increasing seq: only 5 may pass (5 msgs / 5s).
	for i := 0; i < 6; i++ {
		sendChatFrom(t, s, addr, uint32(60+i), 9201, "spam")
	}
	if got := countOpcodePackets(sink, protocol.OpChatText); got != 5 {
		t.Fatalf("rate limit violated: got %d broadcasts for 6 rapid msgs, want 5", got)
	}
}
