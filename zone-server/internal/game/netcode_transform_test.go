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

// newMover inserts a connected session used by transform-handling tests.
func newMover(t *testing.T, s *Server, id uint32, port int, uuid string, pos [3]float32) *network.PlayerSession {
	t.Helper()
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	sess := &network.PlayerSession{
		SessionID:    id,
		AccountID:    uuid,
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		Position:     pos,
		Health:       100,
		LastSeen:     time.Now(),
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

// Movement is client-authoritative: an arbitrarily large one-packet jump is
// accepted, updates session/rotation/anim/gvid/grid state, and produces no
// correction or kick traffic.
func TestTransform_AnyDistanceAccepted(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sess := newMover(t, s, 5101, 43001, "uuid-anywhere", [3]float32{0, 0, 0})
	sink.Reset()

	ct := protocol.ClientTransform{
		PosX:      5000,
		PosY:      -12.5,
		PosZ:      -4000,
		Yaw:       1234,
		Pitch:     -567,
		VelX:      900,
		VelY:      0,
		VelZ:      900,
		AnimFlags: 3,
		Gvid:      77,
	}
	binary.LittleEndian.PutUint32(ct.SessionID[:], sess.SessionID)
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 1, protocol.FlagUnreliable, ct), sess.UDPAddr)

	if got := sessPosition(sess); got != [3]float32{5000, -12.5, -4000} {
		t.Fatalf("position = %v, want the sent position", got)
	}
	sess.Lock()
	rot := sess.Rotation
	vel := sess.Velocity
	anim := sess.AnimFlags
	gvid := sess.Gvid
	sess.Unlock()
	if rot != [2]float32{12.34, -5.67} {
		t.Fatalf("rotation = %v, want [12.34 -5.67]", rot)
	}
	if vel != [3]float32{9, 0, 9} {
		t.Fatalf("velocity = %v, want [9 0 9]", vel)
	}
	if anim != 3 || gvid != 77 {
		t.Fatalf("anim/gvid = %d/%d, want 3/77", anim, gvid)
	}
	if gx, gz, ok := s.grid.GetPosition(sess.SessionID); !ok || gx != 5000 || gz != -4000 {
		t.Fatalf("grid position = (%v,%v,%v), want (5000,-4000,true)", gx, gz, ok)
	}
	if got := len(packetsByOpcode(sink, 0x007B)); got != 0 {
		t.Fatalf("removed OpPositionCorrection still sent: %d packets", got)
	}
	if got := len(packetsByOpcode(sink, protocol.OpDisconnect)); got != 0 {
		t.Fatalf("movement caused %d disconnect packets, want 0", got)
	}
}

// The session-id authentication on transforms is retained: a nonzero mismatched
// id is ignored and never moves the server-side position.
func TestTransform_SessionIDMismatchIgnored(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)
	sess := newMover(t, s, 5102, 43002, "uuid-auth", [3]float32{1, 2, 3})

	ct := protocol.ClientTransform{PosX: 500, PosY: 0, PosZ: 500}
	binary.LittleEndian.PutUint32(ct.SessionID[:], sess.SessionID+1)
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 1, protocol.FlagUnreliable, ct), sess.UDPAddr)

	if got := sessPosition(sess); got != [3]float32{1, 2, 3} {
		t.Fatalf("mismatched session id moved position to %v", got)
	}
}

// A level change still hard-resets position to the server-known spawn and
// updates the grid; there is no movement validation involved.
func TestLevelChange_ResetsPositionToSpawn(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)
	sess := newMover(t, s, 6101, 44001, "uuid-level-reset", [3]float32{100, 5, 100})

	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], "l02_garbage")
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 2, protocol.FlagReliable, lvl), sess.UDPAddr)

	sess.Lock()
	pos := sess.Position
	level := sess.CurrentLevel
	sess.Unlock()
	if pos != defaultLevelSpawn {
		t.Fatalf("position after level change = %v, want spawn %v", pos, defaultLevelSpawn)
	}
	if level != "l02_garbage" {
		t.Fatalf("level after level change = %q, want l02_garbage", level)
	}
	if gx, gz, ok := s.grid.GetPosition(sess.SessionID); !ok || gx != defaultLevelSpawn[0] || gz != defaultLevelSpawn[2] {
		t.Fatalf("grid after level change = (%v,%v,%v), want spawn", gx, gz, ok)
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
