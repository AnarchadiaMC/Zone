package game

import (
	"bytes"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

type fakeSink struct {
	mu   sync.Mutex
	sent [][]byte
}

func (f *fakeSink) UDPListener() *network.UDPListener {
	activeSink = f
	l, _ := network.NewUDPListener(0, nil)
	return l
}

func (f *fakeSink) Record(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := make([]byte, len(data))
	copy(b, data)
	f.sent = append(f.sent, b)
}

func (f *fakeSink) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = nil
}

func (f *fakeSink) LastPacket() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return nil
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeSink) PacketCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func setupTestServerWithDB(t *testing.T) (*Server, *fakeSink, *database.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	// In-memory SQLite requires a single connection so all operations share the same schema and data.
	db.RawDB().SetMaxOpenConns(1)

	sink := &fakeSink{}
	activeSink = sink

	logger := zap.NewNop()
	s := NewServer(nil, db, logger)
	s.udp = sink.UDPListener()

	return s, sink, db
}

func buildTestPacket(t *testing.T, op uint16, seq uint32, flags uint8, payload interface{}) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := protocol.WritePacket(&buf, op, seq, flags, payload); err != nil {
		t.Fatalf("buildTestPacket: WritePacket failed: %v", err)
	}
	return buf.Bytes()
}

// ─────────────────────────────────────────────────────────────────────────────
// HandlePacket Tests (Handshake, Heartbeat, Chat, Transform)
// ─────────────────────────────────────────────────────────────────────────────

func TestHandlePacket_Handshake(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001}
	var req protocol.HandshakeReq
	copy(req.UUID[:], "test-uuid-handshake-1")
	copy(req.Nickname[:], "Stalker1")
	// Zone wire protocol version (protocol.ProtocolVer == 1).
	// NOTE: xrRazom co-op uses protocol 89 — different protocol, must not be
	// accepted here.
	req.ProtocolVer = protocol.ProtocolVer

	raw := buildTestPacket(t, protocol.OpHandshakeReq, 1, protocol.FlagReliable, req)
	s.HandlePacket(raw, addr)

	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatalf("expected session to be created for addr %s", addr.String())
	}
	if sess.AccountID != "test-uuid-handshake-1" {
		t.Errorf("expected AccountID 'test-uuid-handshake-1', got '%s'", sess.AccountID)
	}

	if sink.PacketCount() == 0 {
		t.Fatal("expected HandshakeRes packet sent, got none")
	}

	var rawRes []byte
	sink.mu.Lock()
	for i := len(sink.sent) - 1; i >= 0; i-- {
		rr := bytes.NewReader(sink.sent[i])
		h, err := protocol.ReadHeader(rr)
		if err != nil || h.Opcode != protocol.OpHandshakeRes {
			continue
		}
		rawRes = sink.sent[i]
		break
	}
	sink.mu.Unlock()
	if rawRes == nil {
		t.Fatal("expected HandshakeRes packet sent, got none")
	}
	r := bytes.NewReader(rawRes)
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("failed to read header: %v", err)
	}
	if hdr.Opcode != protocol.OpHandshakeRes {
		t.Errorf("expected OpHandshakeRes (0x0002), got 0x%04X", hdr.Opcode)
	}
}

func TestHandlePacket_Heartbeat_RegisteredSession(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10002}
	sess := &network.PlayerSession{
		SessionID: 10,
		AccountID: "test-uuid-hb",
		UDPAddr:   addr,
		LastSeen:  time.Now().Add(-5 * time.Second),
	}
	s.sessions.AddSession(sess)
	sink.Reset()

	hb := protocol.HeartbeatPayload{Timestamp: 12345678}
	raw := buildTestPacket(t, protocol.OpHeartbeat, 2, protocol.FlagUnreliable, hb)
	s.HandlePacket(raw, addr)

	sess.Lock()
	lastSeen := sess.LastSeen
	sess.Unlock()
	if time.Since(lastSeen) > time.Second {
		t.Errorf("expected LastSeen to be updated, but was %v ago", time.Since(lastSeen))
	}

	if sink.PacketCount() == 0 {
		t.Fatal("expected heartbeat echo to registered session, got none")
	}

	r := bytes.NewReader(sink.LastPacket())
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("failed to read header: %v", err)
	}
	if hdr.Opcode != protocol.OpHeartbeat {
		t.Errorf("expected OpHeartbeat (0x0004), got 0x%04X", hdr.Opcode)
	}
}

// S-08: Ensure unknown/unregistered heartbeat sender does NOT receive an echo.
func TestHandlePacket_Heartbeat_UnregisteredDrop(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	unknownAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10099}
	hb := protocol.HeartbeatPayload{Timestamp: 999999}
	raw := buildTestPacket(t, protocol.OpHeartbeat, 1, protocol.FlagUnreliable, hb)

	s.HandlePacket(raw, unknownAddr)

	if sink.PacketCount() != 0 {
		t.Fatalf("S-08 security violation: expected 0 packets sent for unregistered heartbeat, got %d", sink.PacketCount())
	}
}

func TestHandlePacket_Chat_SanitizationAndBroadcast(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	addr1 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10010}
	addr2 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10011}
	sess1 := &network.PlayerSession{SessionID: 1, AccountID: "user1", UDPAddr: addr1}
	sess2 := &network.PlayerSession{SessionID: 2, AccountID: "user2", UDPAddr: addr2}
	s.sessions.AddSession(sess1)
	s.sessions.AddSession(sess2)
	sink.Reset()

	// Text with control characters: newline and tab should stay, bell/null stripped.
	rawText := "Hello\x00World!\x07\tNew\nLine"
	pkt := protocol.NewChatText(1, rawText)
	raw := buildTestPacket(t, protocol.OpChatText, 5, protocol.FlagReliable, pkt)

	s.HandlePacket(raw, addr1)

	// Both sessions should receive broadcast
	if sink.PacketCount() < 2 {
		t.Fatalf("expected at least 2 broadcast packets, got %d", sink.PacketCount())
	}

	sanitized := sanitizeChatText(rawText)
	expected := "HelloWorld!\tNew\nLine"
	if sanitized != expected {
		t.Errorf("sanitizeChatText mismatch: want %q, got %q", expected, sanitized)
	}
}

func TestHandlePacket_Transform_ValidAndInvalidBounds(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10020}
	sess := &network.PlayerSession{
		SessionID: 100,
		AccountID: "uuid-transform",
		UDPAddr:   addr,
		Position:  [3]float32{0, 0, 0},
	}
	s.sessions.AddSession(sess)

	// 1. Valid transform (movement is accepted regardless of distance)
	ctValid := protocol.ClientTransform{
		PosX:      2.0,
		PosY:      0.5,
		PosZ:      2.0,
		Yaw:       4500,
		Pitch:     0,
		VelX:      300,
		VelY:      0,
		VelZ:      300,
		AnimFlags: 1,
	}
	rawValid := buildTestPacket(t, protocol.OpClientTransform, 1, protocol.FlagUnreliable, ctValid)
	s.HandlePacket(rawValid, addr)

	sess.Lock()
	pos := sess.Position
	sess.Unlock()
	if pos[0] != 2.0 || pos[1] != 0.5 || pos[2] != 2.0 {
		t.Errorf("expected position [2, 0.5, 2], got %v", pos)
	}

	// 2. S-09: Invalid transform with NaN
	ctNaN := protocol.ClientTransform{
		PosX: float32(math.NaN()),
		PosY: 2.0,
		PosZ: 10.0,
	}
	rawNaN := buildTestPacket(t, protocol.OpClientTransform, 2, protocol.FlagUnreliable, ctNaN)
	s.HandlePacket(rawNaN, addr)

	sess.Lock()
	posAfterNaN := sess.Position
	sess.Unlock()
	if posAfterNaN[0] != 2.0 {
		t.Errorf("S-09 violation: position updated with NaN! Got %v", posAfterNaN)
	}

	// 3. S-09: Invalid transform with Infinity
	ctInf := protocol.ClientTransform{
		PosX: float32(math.Inf(1)),
		PosY: 2.0,
		PosZ: 10.0,
	}
	rawInf := buildTestPacket(t, protocol.OpClientTransform, 3, protocol.FlagUnreliable, ctInf)
	s.HandlePacket(rawInf, addr)

	sess.Lock()
	posAfterInf := sess.Position
	sess.Unlock()
	if posAfterInf[0] != 2.0 {
		t.Errorf("S-09 violation: position updated with Inf! Got %v", posAfterInf)
	}

	// 4. S-09: Invalid transform out of bounds (> 10000)
	ctOOB := protocol.ClientTransform{
		PosX: 50000.0,
		PosY: 0.0,
		PosZ: 0.0,
	}
	rawOOB := buildTestPacket(t, protocol.OpClientTransform, 4, protocol.FlagUnreliable, ctOOB)
	s.HandlePacket(rawOOB, addr)

	sess.Lock()
	posAfterOOB := sess.Position
	sess.Unlock()
	if posAfterOOB[0] != 2.0 {
		t.Errorf("S-09 violation: position updated with out of bounds coord! Got %v", posAfterOOB)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// S-01: Tick Play Time Increments
// ─────────────────────────────────────────────────────────────────────────────

// S-01: play time must be credited once per REAL second at any tick rate.
func TestTick_PlayTimeIncrement_TimeBased(t *testing.T) {
	for _, hz := range []int{20, 30, 60} {
		t.Run(fmt.Sprintf("%dHz", hz), func(t *testing.T) {
			s, _, _ := setupTestServerWithDB(t)

			dbQueue := make(chan *database.DBWriteJob, 100)
			s.dbQueue = dbQueue

			addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10030 + hz}
			sess := &network.PlayerSession{
				SessionID: 300,
				AccountID: "uuid-stats-test",
				UDPAddr:   addr,
				LastSeen:  time.Now(),
			}
			s.sessions.AddSession(sess)

			interval := time.Second / time.Duration(hz)
			base := time.Now()

			// First tick seeds the credit clock; nothing is credited yet.
			s.Tick(base)
			if len(dbQueue) != 0 {
				t.Fatalf("%dHz: QueuePeriodicStats before one real second, queue=%d", hz, len(dbQueue))
			}

			// Ticks strictly inside the first second must not credit.
			for i := 1; i < hz; i++ {
				s.Tick(base.Add(time.Duration(i) * interval))
			}
			if len(dbQueue) != 0 {
				t.Fatalf("%dHz: credited play time before a full second elapsed (queue=%d)", hz, len(dbQueue))
			}

			// The tick at exactly +1 s credits exactly 1 second.
			s.Tick(base.Add(time.Second))
			if len(dbQueue) != 1 {
				t.Fatalf("%dHz: expected exactly 1 credit after 1 s, got %d", hz, len(dbQueue))
			}
			job := <-dbQueue
			if !bytes.Contains([]byte(job.Query), []byte("play_time_sec = play_time_sec + ?")) {
				t.Errorf("%dHz: unexpected query queued: %s", hz, job.Query)
			}
			if delta, ok := job.Args[0].(int); !ok || delta != 1 {
				t.Errorf("%dHz: play time delta = %v, want 1", hz, job.Args[0])
			}

			// A second full second credits again, exactly once.
			for i := hz + 1; i < 2*hz; i++ {
				s.Tick(base.Add(time.Duration(i) * interval))
			}
			if len(dbQueue) != 0 {
				t.Fatalf("%dHz: credited before the second full second (queue=%d)", hz, len(dbQueue))
			}
			s.Tick(base.Add(2 * time.Second))
			if len(dbQueue) != 1 {
				t.Fatalf("%dHz: expected exactly 1 credit after 2 s, got %d", hz, len(dbQueue))
			}
		})
	}
}

func TestTick_StaleSessionTimeout(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10031}
	sess := &network.PlayerSession{
		SessionID: 301,
		AccountID: "stale-user",
		UDPAddr:   addr,
		LastSeen:  time.Now().Add(-35 * time.Second),
	}
	s.sessions.AddSession(sess)

	s.Tick(time.Now())

	if remaining := s.sessions.GetByID(301); remaining != nil {
		t.Errorf("expected stale session to be timed out and removed, but still exists")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// S-16 & S-24: KickSession and BanPlayer
// ─────────────────────────────────────────────────────────────────────────────

func TestKickSession_FlushesStateAndDisconnects(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	_ = db.AutoProvision("uuid-kick-test", "hwid-kick", "KickStalker")
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10040}
	sess := &network.PlayerSession{
		SessionID: 400,
		AccountID: "uuid-kick-test",
		UDPAddr:   addr,
		Position:  [3]float32{12.5, 3.0, 45.0},
		Rotation:  [2]float32{1.57, 0},
		Health:    75.0,
		LastSeen:  time.Now(),
	}
	s.sessions.AddSession(sess)

	kicked := s.KickSession(400)
	if !kicked {
		t.Fatal("expected KickSession to return true")
	}

	if s.sessions.GetByID(400) != nil {
		t.Errorf("expected session 400 to be removed from session manager")
	}

	// Check S-16: state flushed to DB
	char, err := db.LoadCharacter("uuid-kick-test")
	if err != nil {
		t.Fatalf("LoadCharacter: %v", err)
	}
	if char.PosX != 12.5 || char.PosY != 3.0 || char.PosZ != 45.0 {
		t.Errorf("expected DB pos [12.5,3,45], got [%f,%f,%f]", char.PosX, char.PosY, char.PosZ)
	}
	if char.Health != 75.0 {
		t.Errorf("expected DB health 75, got %f", char.Health)
	}

	// Check disconnect packet sent
	if sink.PacketCount() == 0 {
		t.Fatal("expected OpDisconnect packet to be sent, got none")
	}
	r := bytes.NewReader(sink.LastPacket())
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("failed to read disconnect header: %v", err)
	}
	if hdr.Opcode != protocol.OpDisconnect {
		t.Errorf("expected OpDisconnect (0x0003), got 0x%04X", hdr.Opcode)
	}
}

func TestBanPlayer_SavesStateKicksAndBreaksLoop(t *testing.T) {
	s, _, db := setupTestServerWithDB(t)

	targetUUID := "uuid-ban-target"
	otherUUID := "uuid-ban-other"

	// Provision accounts in DB
	if err := db.AutoProvision(targetUUID, "hwid-1", "BannedStalker"); err != nil {
		t.Fatalf("failed to auto provision target: %v", err)
	}
	if err := db.AutoProvision(otherUUID, "hwid-2", "OtherStalker"); err != nil {
		t.Fatalf("failed to auto provision other: %v", err)
	}

	sessTarget := &network.PlayerSession{
		SessionID: 501,
		AccountID: targetUUID,
		UDPAddr:   &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10051},
		Position:  [3]float32{10, 20, 30},
		Health:    90.0,
		LastSeen:  time.Now(),
	}
	sessOther := &network.PlayerSession{
		SessionID: 502,
		AccountID: otherUUID,
		UDPAddr:   &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10052},
		Position:  [3]float32{40, 50, 60},
		Health:    100.0,
		LastSeen:  time.Now(),
	}
	s.sessions.AddSession(sessTarget)
	s.sessions.AddSession(sessOther)

	if err := s.BanPlayer(targetUUID, "speedhack"); err != nil {
		t.Fatalf("BanPlayer failed: %v", err)
	}

	// Verify target was removed
	if s.sessions.GetByID(501) != nil {
		t.Errorf("expected banned session to be removed")
	}
	// Verify other session was untouched
	if s.sessions.GetByID(502) == nil {
		t.Errorf("other session should not have been removed")
	}

	// Verify account is marked banned in DB
	banned, reason, err := db.IsPlayerBanned(targetUUID)
	if err != nil {
		t.Fatalf("IsPlayerBanned: %v", err)
	}
	if !banned {
		t.Errorf("expected account %s to be marked banned in DB", targetUUID)
	}
	if reason != "speedhack" {
		t.Errorf("expected ban reason 'speedhack', got %q", reason)
	}

	// Verify S-16: character state flushed to DB
	char, err := db.LoadCharacter(targetUUID)
	if err != nil {
		t.Fatalf("LoadCharacter: %v", err)
	}
	if char.PosX != 10 || char.PosY != 20 || char.PosZ != 30 {
		t.Errorf("expected DB pos [10,20,30], got [%f,%f,%f]", char.PosX, char.PosY, char.PosZ)
	}
	if char.Health != 90.0 {
		t.Errorf("expected DB health 90, got %f", char.Health)
	}
}
