package game

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

func newMover(t *testing.T, s *Server, id uint32, port int, uuid string, pos [3]float32, lastTransform, heartbeat time.Time) *network.PlayerSession {
	t.Helper()
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	sess := &network.PlayerSession{
		SessionID:         id,
		AccountID:         uuid,
		UDPAddr:           addr,
		CurrentLevel:      "l01_escape",
		Position:          pos,
		Health:            100,
		LastSeen:          heartbeat,
		LastHeartbeat:     heartbeat,
		LastTransformTime: lastTransform,
	}
	s.sessions.AddSession(sess)
	s.grid.Insert(id, pos[0], pos[2])
	return sess
}

func sendTransform(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, x, y, z float32) {
	t.Helper()
	ct := protocol.ClientTransform{PosX: x, PosY: y, PosZ: z}
	binary.LittleEndian.PutUint32(ct.SessionID[:], sess.SessionID)
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, seq, protocol.FlagUnreliable, ct), sess.UDPAddr)
}

func sessPosition(sess *network.PlayerSession) [3]float32 {
	sess.Lock()
	defer sess.Unlock()
	return sess.Position
}

func TestMovementGuard_NormalMovementAccepted(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	now := time.Now()
	sess := newMover(t, s, 5101, 43001, "uuid-normal", [3]float32{0, 0, 0}, now.Add(-time.Second), now.Add(-time.Second))
	sink.Reset()

	sendTransform(t, s, sess, 1, 10, 0, 0)

	if got := sessPosition(sess); got[0] != 10 {
		t.Fatalf("position = %v, want x=10", got)
	}
	if got := len(packetsByOpcode(sink, protocol.OpPositionCorrection)); got != 0 {
		t.Fatalf("normal movement produced %d correction packets, want 0", got)
	}
	if got := s.anticheat.ViolationCount(sess.SessionID); got != 0 {
		t.Fatalf("normal movement recorded %d violations, want 0", got)
	}
}

func TestMovementGuard_TeleportRejectedWithCorrection(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	now := time.Now()
	sess := newMover(t, s, 5102, 43002, "uuid-teleport", [3]float32{5, 0, 5}, now.Add(-20*time.Millisecond), now)
	sink.Reset()

	sendTransform(t, s, sess, 1, 105, 0, 5)

	if got := sessPosition(sess); got[0] != 5 || got[2] != 5 {
		t.Fatalf("teleport changed server position to %v, want last valid (5,*,5)", got)
	}
	corrections := packetsByOpcode(sink, protocol.OpPositionCorrection)
	if len(corrections) != 1 {
		t.Fatalf("correction packets = %d, want 1", len(corrections))
	}
	var pc protocol.PositionCorrection
	if err := binary.Read(bytes.NewReader(packetPayload(t, corrections[0])), binary.LittleEndian, &pc); err != nil {
		t.Fatalf("decode PositionCorrection: %v", err)
	}
	if pc.X != 5 || pc.Z != 5 {
		t.Fatalf("correction = %+v, want last valid (5,0,5)", pc)
	}
	if got := s.anticheat.ViolationCount(sess.SessionID); got != 1 {
		t.Fatalf("violations = %d, want 1", got)
	}
}

func TestNetcode_LagSwitchStrikeOnGapThenBurst(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)
	now := time.Now()
	sess := newMover(t, s, 5201, 43011, "uuid-lagswitch", [3]float32{0, 0, 0}, now.Add(-100*time.Millisecond), now)

	// Seed the history ring with one accepted transform.
	sendTransform(t, s, sess, 1, 1, 0, 0)

	// Simulate a 2 s transform stall while heartbeats kept the session alive,
	// then a burst jump when the stream resumes.
	sess.Lock()
	sess.LastTransformTime = time.Now().Add(-2 * time.Second)
	sess.Unlock()
	sendTransform(t, s, sess, 2, 100, 0, 0)

	if got := s.anticheat.LagswitchStrikeCount(sess.SessionID, time.Now()); got != 1 {
		t.Fatalf("lag-switch strikes = %d, want 1", got)
	}
}

func TestNetcode_GapBelowThresholdNoStrike(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)
	now := time.Now()
	sess := newMover(t, s, 5202, 43012, "uuid-below-gap", [3]float32{0, 0, 0}, now.Add(-100*time.Millisecond), now)
	sendTransform(t, s, sess, 1, 1, 0, 0)

	// 1 s gap (< 1.5 s threshold) with a physically valid 20 m move.
	sess.Lock()
	sess.LastTransformTime = time.Now().Add(-1 * time.Second)
	sess.Unlock()
	sendTransform(t, s, sess, 2, 21, 0, 0)

	if got := s.anticheat.LagswitchStrikeCount(sess.SessionID, time.Now()); got != 0 {
		t.Fatalf("below-threshold gap recorded %d strikes, want 0", got)
	}
	if got := sessPosition(sess); got[0] != 21 {
		t.Fatalf("valid delayed move rejected: position = %v, want x=21", got)
	}
}

// A long gap with no heartbeat/ACK during it is plain packet loss, not a
// lag-switch signature: no strike (conservative, never punishes loss).
func TestNetcode_PlainPacketLossNoStrike(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)
	now := time.Now()
	sess := newMover(t, s, 5203, 43013, "uuid-loss", [3]float32{0, 0, 0}, now.Add(-100*time.Millisecond), now)
	sendTransform(t, s, sess, 1, 1, 0, 0)

	old := time.Now().Add(-3 * time.Second)
	sess.Lock()
	sess.LastTransformTime = old
	sess.LastHeartbeat = old
	sess.LastSeen = old
	sess.Unlock()
	sendTransform(t, s, sess, 2, 60, 0, 0)

	if got := s.anticheat.LagswitchStrikeCount(sess.SessionID, time.Now()); got != 0 {
		t.Fatalf("plain loss recorded %d strikes, want 0", got)
	}
	if got := s.anticheat.ViolationCount(sess.SessionID); got != 1 {
		t.Fatalf("burst after loss violations = %d, want 1 (movement only)", got)
	}
}

func TestNetcode_LagSwitchEscalatesToKickWithAudit(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	now := time.Now()
	sess := newMover(t, s, 5301, 43021, "uuid-ls-escalate", [3]float32{0, 0, 0}, now.Add(-100*time.Millisecond), now)
	sendTransform(t, s, sess, 1, 1, 0, 0)
	sink.Reset()

	seq := uint32(2)
	for i := 1; i <= 3; i++ {
		sess.Lock()
		sess.LastTransformTime = time.Now().Add(-2 * time.Second)
		sess.Unlock()
		sendTransform(t, s, sess, seq, 100, 0, 0)
		seq++
	}

	if got := s.sessions.GetByID(sess.SessionID); got != nil {
		t.Fatalf("session survived %d lag-switch strikes, want kick", 3)
	}
	if got := len(packetsByOpcode(sink, protocol.OpDisconnect)); got != 1 {
		t.Fatalf("kick did not send OpDisconnect: got %d", got)
	}

	var detail string
	err := db.RawDB().QueryRow(
		"SELECT detail FROM audit_log WHERE event_type='lagswitch_kick' ORDER BY id DESC LIMIT 1").Scan(&detail)
	if err != nil {
		t.Fatalf("lag-switch kick audit row missing: %v", err)
	}
	if !bytes.Contains([]byte(detail), []byte("network abuse suspected")) {
		t.Fatalf("audit detail = %q, want 'network abuse suspected'", detail)
	}
}

func TestItemActionMalformedPayloadIgnored(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-malformed", "hwid-malformed", "Malformed"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	sess := ledgerSession(t, s, 6801, 43031, "uuid-malformed", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	// Header + 87-byte payload (one short of the frozen 88).
	var raw bytes.Buffer
	hdr := protocol.PacketHeader{
		Magic:         protocol.HeaderMagic,
		Protocol:      protocol.ProtocolVer,
		FlagsChannel:  protocol.FlagReliable,
		SequenceNum:   1,
		Opcode:        protocol.OpItemAction,
		PayloadLength: 87,
	}
	if err := binary.Write(&raw, binary.LittleEndian, &hdr); err != nil {
		t.Fatalf("header write: %v", err)
	}
	raw.Write(make([]byte, 87))
	s.HandlePacket(raw.Bytes(), sess.UDPAddr)

	if got := len(packetsByOpcode(sink, protocol.OpItemUpdate)); got != 0 {
		t.Fatalf("malformed OpItemAction produced %d item updates, want 0", got)
	}
}

func BenchmarkMovementGuard_ValidDisplacement(b *testing.B) {
	g := NewMovementGuard(nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !g.ValidDisplacement(10, 0.5) {
			b.Fatal("unexpected rejection")
		}
	}
}
