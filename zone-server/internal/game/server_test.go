package game

import (
	"bytes"
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

func newFakeSessionManager() *network.SessionManager {
	return network.NewSessionManager()
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

	r := bytes.NewReader(sink.LastPacket())
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

	// 1. Valid transform
	ctValid := protocol.ClientTransform{
		PosX:      50.0,
		PosY:      2.0,
		PosZ:      120.0,
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
	if pos[0] != 50.0 || pos[1] != 2.0 || pos[2] != 120.0 {
		t.Errorf("expected position [50, 2, 120], got %v", pos)
	}

	// 2. S-09: Invalid transform with NaN
	ctNaN := protocol.ClientTransform{
		PosX: float32(math.NaN()),
		PosY: 2.0,
		PosZ: 120.0,
	}
	rawNaN := buildTestPacket(t, protocol.OpClientTransform, 2, protocol.FlagUnreliable, ctNaN)
	s.HandlePacket(rawNaN, addr)

	sess.Lock()
	posAfterNaN := sess.Position
	sess.Unlock()
	if posAfterNaN[0] != 50.0 {
		t.Errorf("S-09 violation: position updated with NaN! Got %v", posAfterNaN)
	}

	// 3. S-09: Invalid transform with Infinity
	ctInf := protocol.ClientTransform{
		PosX: float32(math.Inf(1)),
		PosY: 2.0,
		PosZ: 120.0,
	}
	rawInf := buildTestPacket(t, protocol.OpClientTransform, 3, protocol.FlagUnreliable, ctInf)
	s.HandlePacket(rawInf, addr)

	sess.Lock()
	posAfterInf := sess.Position
	sess.Unlock()
	if posAfterInf[0] != 50.0 {
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
	if posAfterOOB[0] != 50.0 {
		t.Errorf("S-09 violation: position updated with out of bounds coord! Got %v", posAfterOOB)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// S-01: Tick Play Time Increments
// ─────────────────────────────────────────────────────────────────────────────

func TestTick_PlayTimeIncrement_30Hz(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	dbQueue := make(chan *database.DBWriteJob, 100)
	s.dbQueue = dbQueue

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10030}
	sess := &network.PlayerSession{
		SessionID: 300,
		AccountID: "uuid-stats-test",
		UDPAddr:   addr,
		LastSeen:  time.Now(),
	}
	s.sessions.AddSession(sess)

	// Run 29 ticks
	for i := 0; i < 29; i++ {
		s.Tick(time.Now())
	}
	// At tick 29, queue should be empty (no stats queued yet)
	if len(dbQueue) > 0 {
		t.Fatalf("S-01 violation: QueuePeriodicStats called before 30th tick! Queue len = %d", len(dbQueue))
	}

	// 30th tick should trigger QueuePeriodicStats
	s.Tick(time.Now())
	if len(dbQueue) != 1 {
		t.Fatalf("expected exactly 1 DB write job at tick 30, got %d", len(dbQueue))
	}

	job := <-dbQueue
	if !bytes.Contains([]byte(job.Query), []byte("play_time_sec = play_time_sec + ?")) {
		t.Errorf("unexpected query queued: %s", job.Query)
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
	s, sink, _ := setupTestServerWithDB(t)
	dbQueue := make(chan *database.DBWriteJob, 100)
	s.dbQueue = dbQueue
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

	// Check S-16: state flushed to dbQueue
	if len(dbQueue) == 0 {
		t.Fatal("S-16 violation: character state was not queued to database before kick")
	}
	job := <-dbQueue
	if !bytes.Contains([]byte(job.Query), []byte("UPDATE characters SET pos_x=?")) {
		t.Errorf("unexpected query: %s", job.Query)
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
	dbQueue := make(chan *database.DBWriteJob, 100)
	s.dbQueue = dbQueue

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

	// Verify S-16: character state queued to dbQueue
	if len(dbQueue) == 0 {
		t.Fatal("S-16 violation: character state was not queued to database before ban/kick")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// S-13: Anti-Cheat 3D Speed Magnitude Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestAntiCheat_ValidateMove_3DSpeedMagnitude(t *testing.T) {
	ac := NewAntiCheatManager()

	sess := &network.PlayerSession{
		SessionID: 601,
		Position:  [3]float32{0, 0, 0},
	}

	// 1. Normal horizontal move: 10m in 1s = 10 m/s <= 25 m/s -> valid
	valid, reason := ac.ValidateMove(sess, [3]float32{10, 0, 0}, 1.0)
	if !valid {
		t.Errorf("expected normal move to be valid, got false: %s", reason)
	}

	// 2. Normal 3D move: dx=10, dy=5, dz=10, dt=1.0 -> sqrt(100+25+100) = sqrt(225) = 15 m/s <= 25 m/s -> valid
	valid, reason = ac.ValidateMove(sess, [3]float32{10, 5, 10}, 1.0)
	if !valid {
		t.Errorf("expected normal 3D move to be valid, got false: %s", reason)
	}

	// 3. S-13 Violation: 3D diagonal speed hack:
	// dx=18, dy=10, dz=18 -> 18^2+10^2+18^2 = 324+100+324 = 748 -> sqrt(748) ~ 27.35 m/s > 25 m/s!
	valid, reason = ac.ValidateMove(sess, [3]float32{18, 10, 18}, 1.0)
	if valid {
		t.Errorf("S-13 violation: 3D speed > 25 m/s was accepted as valid!")
	}
	if reason == "" {
		t.Errorf("expected non-empty reason for speed violation")
	}

	// 4. Vertical teleport: dy=20m > 15m
	valid, reason = ac.ValidateMove(sess, [3]float32{0, 20, 0}, 1.0)
	if valid {
		t.Errorf("expected vertical teleport to be rejected")
	}
	if reason != "vertical teleport" {
		t.Errorf("expected 'vertical teleport', got %q", reason)
	}
}
