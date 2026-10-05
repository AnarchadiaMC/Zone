package game

import (
	"bytes"
	"net"
	"testing"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// ENTITY_ENTER/LEAVE_AOI must travel through the server's reliable send path:
// the packet is recorded, carries FlagReliable and is enqueued for
// retransmission (a lost enter cannot leave a ghost peer invisible).
func TestAoIManager_NotifyEntityReliable(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9999}
	session := &network.PlayerSession{
		SessionID: 100,
		AccountID: "uuid-aoi-reliable",
		UDPAddr:   addr,
	}
	s.sessions.AddSession(session)
	sink.Reset()

	info := EntityInfo{
		ID:      42,
		Type:    2,
		Section: "stalker_regular",
		Pos:     [3]float32{10, 20, 30},
		Faction: "stalker",
		Health:  100,
		Name:    "Regular",
	}

	// First enter registers the edge, sends exactly one reliable packet and
	// enqueues it for retransmission.
	s.aoi.NotifyEntityEnter(session, info, s)
	enters := packetsByOpcode(sink, protocol.OpEntityEnterAoI)
	if len(enters) != 1 {
		t.Fatalf("enter packets = %d, want 1", len(enters))
	}
	hdr, err := protocol.ReadHeader(bytes.NewReader(enters[0]))
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.FlagsChannel&protocol.FlagReliable == 0 {
		t.Fatalf("ENTITY_ENTER not sent reliable: flags=0x%02X", hdr.FlagsChannel)
	}
	if !s.aoi.entries[100][42] {
		t.Fatalf("entity 42 not registered in AoI entries")
	}
	if s.ackQueue.Len() != 1 {
		t.Fatalf("ackQueue length = %d, want 1 (reliable retransmit entry)", s.ackQueue.Len())
	}

	// Duplicate enter is suppressed: no new packet, no new retransmit entry.
	s.aoi.NotifyEntityEnter(session, info, s)
	if got := len(packetsByOpcode(sink, protocol.OpEntityEnterAoI)); got != 1 {
		t.Fatalf("duplicate enter produced %d packets, want 1", got)
	}
	if s.ackQueue.Len() != 1 {
		t.Fatalf("duplicate enter changed ackQueue length to %d, want 1", s.ackQueue.Len())
	}

	// Leave removes the edge and sends one reliable packet.
	s.aoi.NotifyEntityLeave(session, 42, s)
	if s.aoi.entries[100][42] {
		t.Fatalf("entity 42 still registered after leave")
	}
	leaves := packetsByOpcode(sink, protocol.OpEntityLeaveAoI)
	if len(leaves) != 1 {
		t.Fatalf("leave packets = %d, want 1", len(leaves))
	}
	hdr, err = protocol.ReadHeader(bytes.NewReader(leaves[0]))
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.FlagsChannel&protocol.FlagReliable == 0 {
		t.Fatalf("ENTITY_LEAVE not sent reliable: flags=0x%02X", hdr.FlagsChannel)
	}
	if s.ackQueue.Len() != 2 {
		t.Fatalf("ackQueue length = %d, want 2 (enter + leave pending)", s.ackQueue.Len())
	}
}
