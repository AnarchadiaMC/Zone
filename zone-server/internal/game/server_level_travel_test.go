package game

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// addTravelClient inserts a connected session with a gvid and a known visual so
// it is announceable for player ENTITY_ENTER exchange.
func addTravelClient(t *testing.T, s *Server, id uint32, port int, level string, pos [3]float32, gvid uint16) *network.PlayerSession {
	t.Helper()
	sess := &network.PlayerSession{
		SessionID:    id,
		AccountID:    fmt.Sprintf("uuid-travel-%d", id),
		Name:         fmt.Sprintf("Traveller%d", id),
		UDPAddr:      &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port},
		CurrentLevel: level,
		Position:     pos,
		Health:       100,
		Gvid:         gvid,
		LastSeen:     time.Now(),
	}
	copy(sess.Visual[:], DefaultActorVisual)
	sess.HasVisual = true
	s.sessions.AddSession(sess)
	return sess
}

func sendLevelChange(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, level string) {
	t.Helper()
	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], level)
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, seq, protocol.FlagReliable, lvl), sess.UDPAddr)
}

func sendGvidTransform(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, gvid uint16) {
	t.Helper()
	sess.Lock()
	pos := sess.Position
	sess.Unlock()
	ct := protocol.ClientTransform{PosX: pos[0], PosY: pos[1], PosZ: pos[2], Gvid: gvid}
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, seq, protocol.FlagUnreliable, ct), sess.UDPAddr)
}

func readEntityLeavePayload(t *testing.T, raw []byte) protocol.EntityLeaveAoI {
	t.Helper()
	var leave protocol.EntityLeaveAoI
	if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &leave); err != nil {
		t.Fatalf("read EntityLeaveAoI: %v", err)
	}
	return leave
}

// Two clients on l01: one reports l02. Old-level peers get exactly one leave,
// the mover gets the new level's peers (gvid+visual only) and its own safe-zone
// state; repeating the same-level report produces no further traffic. A third
// l01 client receives nothing from l02.
func TestLevelTravel_LeaveAndEnterExactlyOnce(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	a := addTravelClient(t, s, 8701, 33001, "l01_escape", [3]float32{0, 0, 0}, 0x11)
	_ = addTravelClient(t, s, 8702, 33002, "l01_escape", [3]float32{5, 0, 0}, 0x22)
	c := addTravelClient(t, s, 8703, 33003, "l01_escape", [3]float32{-5, 0, 0}, 0x33)
	d := addTravelClient(t, s, 8704, 33004, "l02_garbage", [3]float32{50, 0, 0}, 0x44)

	// The client-reported position (movement is client-authoritative) is kept:
	// A reports l02 while standing on the Garbage flea-market safe zone.
	a.Lock()
	a.Position = [3]float32{-88.5, -0.5, -8.2}
	a.Unlock()

	sink.Reset()
	sendLevelChange(t, s, a, 1, "l02_garbage")

	// Old-level peers B and C each get one leave; D (already on l02) does not.
	leaves := packetsByOpcode(sink, protocol.OpEntityLeaveAoI)
	if len(leaves) != 2 {
		t.Fatalf("old-level leaves = %d, want exactly 2 (B and C)", len(leaves))
	}
	for _, raw := range leaves {
		if leave := readEntityLeavePayload(t, raw); leave.EntityID != a.SessionID {
			t.Fatalf("leave EntityID = %d, want mover %d", leave.EntityID, a.SessionID)
		}
	}

	// A receives D's ENTITY_ENTER (same-level peer with gvid+visual).
	if got := len(entityEnterPacketsFor(t, sink, d.SessionID)); got != 1 {
		t.Fatalf("mover enters from l02 peer = %d, want 1", got)
	}
	// A's own gvid was reset, so it is not announced until the next transform.
	if got := len(entityEnterPacketsFor(t, sink, a.SessionID)); got != 0 {
		t.Fatalf("mover announced immediately after level change: %d entries", got)
	}

	// Per-level safe-zone state: l02 garbage flea for A's accepted position.
	sz := parseSafezoneState(t, findOpcodePacket(t, sink, protocol.OpSafezoneState))
	if sz.Locked != 1 || nullTermString(sz.ZoneID[:]) != "sz_garbage_flea" {
		t.Fatalf("mover safezone = locked %d zone %q, want 1/sz_garbage_flea", sz.Locked, nullTermString(sz.ZoneID[:]))
	}

	// Repeating the same-level report is not a transition: no extra leave.
	sink.Reset()
	sendLevelChange(t, s, a, 2, "l02_garbage")
	if got := len(packetsByOpcode(sink, protocol.OpEntityLeaveAoI)); got != 0 {
		t.Fatalf("same-level repeat produced %d leaves, want 0", got)
	}
	if got := len(entityEnterPacketsFor(t, sink, d.SessionID)); got != 0 {
		t.Fatalf("same-level repeat produced %d enters, want 0", got)
	}

	// Re-establishing gvid announces A to the l02 peer exactly once; the l01
	// client C is never a recipient.
	sink.Reset()
	sendGvidTransform(t, s, a, 3, 0x1111)
	entersA := entityEnterPacketsFor(t, sink, a.SessionID)
	if len(entersA) != 1 {
		t.Fatalf("post-transform enters for mover = %d, want exactly 1 (l02 peer only)", len(entersA))
	}
	if entersA[0].Gvid != 0x1111 {
		t.Fatalf("enter gvid = 0x%04X, want 0x1111", entersA[0].Gvid)
	}
	c.Lock()
	cLevel := c.CurrentLevel
	c.Unlock()
	if cLevel != "l01_escape" {
		t.Fatalf("third client level = %q, want l01_escape", cLevel)
	}
}

// Both clients migrate to l02 and then see each other: the second mover gets
// the first mover's enter once the first re-announces, and vice versa. The l01
// client only ever receives the old-level leave.
func TestLevelTravel_BothInNewLevelSeeEachOther(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	a := addTravelClient(t, s, 8711, 33011, "l01_escape", [3]float32{0, 0, 0}, 0x11)
	b := addTravelClient(t, s, 8712, 33012, "l01_escape", [3]float32{5, 0, 0}, 0x22)
	_ = addTravelClient(t, s, 8713, 33013, "l01_escape", [3]float32{-5, 0, 0}, 0x33)

	sendLevelChange(t, s, a, 1, "l02_garbage")
	// B moves into the l02 garbage flea safe zone: its safe-zone state must be
	// computed for l02, not carried over from l01.
	b.Lock()
	b.Position = [3]float32{-88.5, -0.5, -8.2}
	b.Unlock()
	sink.Reset()
	sendLevelChange(t, s, b, 2, "l02_garbage")

	// C (l01) gets one leave for B; A (l02) is not an old-level peer anymore.
	leaves := packetsByOpcode(sink, protocol.OpEntityLeaveAoI)
	if len(leaves) != 1 {
		t.Fatalf("l01 leaves for B = %d, want 1 (C only)", len(leaves))
	}
	sz := parseSafezoneState(t, findOpcodePacket(t, sink, protocol.OpSafezoneState))
	if sz.Locked != 1 || nullTermString(sz.ZoneID[:]) != "sz_garbage_flea" {
		t.Fatalf("B safezone = locked %d zone %q, want 1/sz_garbage_flea", sz.Locked, nullTermString(sz.ZoneID[:]))
	}

	// A re-announces: its only l02 peer (B) sees it exactly once.
	sink.Reset()
	sendGvidTransform(t, s, a, 3, 0x1111)
	if got := len(entityEnterPacketsFor(t, sink, a.SessionID)); got != 1 {
		t.Fatalf("A enters to l02 peers = %d, want 1", got)
	}
	// B re-announces: A now sees B exactly once.
	sendGvidTransform(t, s, b, 4, 0x2222)
	if got := len(entityEnterPacketsFor(t, sink, b.SessionID)); got != 1 {
		t.Fatalf("B enters to l02 peers = %d, want 1", got)
	}

	// The l01 client saw neither mover enter l02.
	total := len(entityEnterPacketsFor(t, sink, a.SessionID)) + len(entityEnterPacketsFor(t, sink, b.SessionID))
	if total != 2 {
		t.Fatalf("total l02 enters = %d, want 2 (no l01 recipients)", total)
	}
}

// Entities meanwhile on the mover's old level do not leak into the new level:
// a lone mover on l02 broadcasts its enter to nobody.
func TestLevelTravel_NoCrossLevelTraffic(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	a := addTravelClient(t, s, 8721, 33021, "l01_escape", [3]float32{0, 0, 0}, 0x11)
	b := addTravelClient(t, s, 8722, 33022, "l01_escape", [3]float32{5, 0, 0}, 0x22)

	sink.Reset()
	sendLevelChange(t, s, a, 1, "l02_garbage")
	// The only traffic is the single old-level leave for B.
	if got := len(packetsByOpcode(sink, protocol.OpEntityLeaveAoI)); got != 1 {
		t.Fatalf("leaves = %d, want 1", got)
	}
	if got := len(entityEnterPacketsFor(t, sink, b.SessionID)); got != 0 {
		t.Fatalf("B (l01) entered A's l02 view: %d enters", got)
	}

	// B remaining on l01 never receives A's post-change announce (A has no
	// same-level peers, so there is no enter to send at all).
	sink.Reset()
	sendGvidTransform(t, s, a, 2, 0xABCD)
	if got := len(entityEnterPacketsFor(t, sink, a.SessionID)); got != 0 {
		t.Fatalf("A's enter broadcast with no l02 peers = %d, want 0", got)
	}
}
