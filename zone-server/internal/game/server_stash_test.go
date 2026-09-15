package game

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"

	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/protocol"

	"go.uber.org/zap"
)

// buildStashInteractPacket creates a fully-framed OpStashInteract UDP payload.
func buildStashInteractPacket(t *testing.T, stashID uint32, action uint8, section string, count uint16) []byte {
	t.Helper()

	pkt := protocol.StashInteractPayload{
		StashID: stashID,
		Action:  action,
		Count:   count,
	}
	copy(pkt.ItemSection[:], section)

	var buf bytes.Buffer
	if err := protocol.WritePacket(&buf, protocol.OpStashInteract, 1, protocol.FlagReliable, pkt); err != nil {
		t.Fatalf("buildStashInteractPacket: WritePacket failed: %v", err)
	}
	return buf.Bytes()
}

// parseResponseFromBytes extracts a StashResponsePayload from raw UDP bytes
// (header + payload).
func parseResponseFromBytes(t *testing.T, data []byte) protocol.StashResponsePayload {
	t.Helper()

	r := bytes.NewReader(data)
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("parseResponseFromBytes: ReadHeader failed: %v", err)
	}
	if hdr.Opcode != protocol.OpStashResponse {
		t.Fatalf("parseResponseFromBytes: expected opcode 0x%04X, got 0x%04X",
			protocol.OpStashResponse, hdr.Opcode)
	}

	var resp protocol.StashResponsePayload
	if err := binary.Read(r, binary.LittleEndian, &resp); err != nil {
		t.Fatalf("parseResponseFromBytes: failed to read payload: %v", err)
	}
	return resp
}

// lastStashResponse scans recorded packets for the last OpStashResponse,
// skipping OpAck packets. Required since HandlePacket now sends an unreliable
// OpAck for every reliable inbound packet before the actual response.
func lastStashResponse(t *testing.T, sent [][]byte) protocol.StashResponsePayload {
	t.Helper()

	for i := len(sent) - 1; i >= 0; i-- {
		r := bytes.NewReader(sent[i])
		hdr, err := protocol.ReadHeader(r)
		if err != nil {
			continue
		}
		if hdr.Opcode == protocol.OpStashResponse {
			var resp protocol.StashResponsePayload
			if err := binary.Read(r, binary.LittleEndian, &resp); err != nil {
				t.Fatalf("lastStashResponse: failed to read payload: %v", err)
			}
			return resp
		}
	}
	t.Fatalf("lastStashResponse: no OpStashResponse found in %d packets", len(sent))
	var empty protocol.StashResponsePayload
	return empty
}

// newTestServer builds a minimal Server wired to a fake UDP sink and an
// in-memory SQLite database.
func newTestServer(t *testing.T) (*Server, *fakeSink, *database.DB) {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("newTestServer: failed to open in-memory db: %v", err)
	}

	logger := zap.NewNop()
	s := &Server{
		sessions: newFakeSessionManager(),
		stashMgr: NewStashManager(db),
		logger:   logger,
	}

	sink := &fakeSink{}
	s.udp = sink.UDPListener()

	return s, sink, db
}

// ─────────────────────────────────────────────────────────────────────────────
// Packet marshal / unmarshal round-trip
// ─────────────────────────────────────────────────────────────────────────────

func TestStashInteractPacketRoundTrip(t *testing.T) {
	section := "wpn_pm"
	stashID := uint32(42)
	count := uint16(3)

	raw := buildStashInteractPacket(t, stashID, 1, section, count)

	r := bytes.NewReader(raw)
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}
	if hdr.Magic != protocol.HeaderMagic {
		t.Errorf("unexpected magic: 0x%04X", hdr.Magic)
	}
	if hdr.Opcode != protocol.OpStashInteract {
		t.Errorf("expected OpStashInteract 0x%04X, got 0x%04X", protocol.OpStashInteract, hdr.Opcode)
	}

	var pkt protocol.StashInteractPayload
	if err := binary.Read(r, binary.LittleEndian, &pkt); err != nil {
		t.Fatalf("binary.Read StashInteractPayload: %v", err)
	}
	if pkt.StashID != stashID {
		t.Errorf("StashID: want %d, got %d", stashID, pkt.StashID)
	}
	if pkt.Action != 1 {
		t.Errorf("Action: want 1, got %d", pkt.Action)
	}
	if pkt.Count != count {
		t.Errorf("Count: want %d, got %d", count, pkt.Count)
	}
	if got := nullTermString(pkt.ItemSection[:]); got != section {
		t.Errorf("ItemSection: want %q, got %q", section, got)
	}
}

func TestStashResponsePacketRoundTrip(t *testing.T) {
	orig := protocol.StashResponsePayload{
		StashID: 7,
		Status:  0,
		Count:   2,
	}
	copy(orig.Data[:], `[{"section":"medkit","count":2}]`)

	var buf bytes.Buffer
	if err := protocol.WritePacket(&buf, protocol.OpStashResponse, 99, protocol.FlagReliable, orig); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	resp := parseResponseFromBytes(t, buf.Bytes())
	if resp.StashID != orig.StashID {
		t.Errorf("StashID: want %d, got %d", orig.StashID, resp.StashID)
	}
	if resp.Status != orig.Status {
		t.Errorf("Status: want %d, got %d", orig.Status, resp.Status)
	}
	if resp.Count != orig.Count {
		t.Errorf("Count: want %d, got %d", orig.Count, resp.Count)
	}
	if resp.Data != orig.Data {
		t.Errorf("Data field mismatch")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Action=1 (Open)
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleOpStashInteract_Open(t *testing.T) {
	s, sink, db := newTestServer(t)

	// Pre-create a stash so OpenStash succeeds.
	stashID := uint32(11)
	items := []StashItem{{Section: "medkit", Count: 3}}
	itemsJSON, _ := json.Marshal(items)
	if err := db.SaveStash(stashID, "l01_escape", 1.0, 2.0, 3.0, string(itemsJSON)); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9001}
	raw := buildStashInteractPacket(t, stashID, 1 /*Open*/, "", 0)
	s.HandlePacket(raw, addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	resp := lastStashResponse(t, sink.sent)
	if resp.StashID != stashID {
		t.Errorf("StashID: want %d, got %d", stashID, resp.StashID)
	}
	if resp.Status != 0 {
		t.Errorf("expected Status=0 (OK), got %d", resp.Status)
	}
	// Data field should contain the stash contents JSON.
	got := nullTermString(resp.Data[:])
	if got == "" {
		t.Errorf("expected stash contents in Data, got empty string")
	}
}

func TestHandleOpStashInteract_Open_NotFound(t *testing.T) {
	s, sink, _ := newTestServer(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9002}
	raw := buildStashInteractPacket(t, 9999 /*missing*/, 1, "", 0)
	s.HandlePacket(raw, addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	resp := lastStashResponse(t, sink.sent)
	if resp.Status != 1 {
		t.Errorf("expected Status=1 (NotFound), got %d", resp.Status)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Action=2 (Take)
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleOpStashInteract_Take(t *testing.T) {
	s, sink, db := newTestServer(t)

	stashID := uint32(22)
	items := []StashItem{{Section: "ammo_9x18_fmj", Count: 10}}
	itemsJSON, _ := json.Marshal(items)
	if err := db.SaveStash(stashID, "l02_garbage", 0, 0, 0, string(itemsJSON)); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9003}
	raw := buildStashInteractPacket(t, stashID, 2 /*Take*/, "ammo_9x18_fmj", 5)
	s.HandlePacket(raw, addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	resp := lastStashResponse(t, sink.sent)
	if resp.Status != 0 {
		t.Errorf("Take: expected Status=0, got %d", resp.Status)
	}

	// Verify stash contents were actually modified.
	mgr := s.stashMgr
	stash, err := mgr.GetStash(stashID)
	if err != nil {
		t.Fatalf("GetStash: %v", err)
	}
	var gotItems []StashItem
	_ = json.Unmarshal(stash.Contents, &gotItems)
	if len(gotItems) != 1 || gotItems[0].Count != 5 {
		t.Errorf("expected 5 ammo after take, got %+v", gotItems)
	}
}

func TestHandleOpStashInteract_Take_InsufficientCount(t *testing.T) {
	s, sink, db := newTestServer(t)

	stashID := uint32(23)
	items := []StashItem{{Section: "bandage", Count: 1}}
	itemsJSON, _ := json.Marshal(items)
	if err := db.SaveStash(stashID, "l01_escape", 0, 0, 0, string(itemsJSON)); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9004}
	// Try to take 99 while only 1 exists.
	raw := buildStashInteractPacket(t, stashID, 2, "bandage", 99)
	s.HandlePacket(raw, addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	resp := lastStashResponse(t, sink.sent)
	if resp.Status != 2 {
		t.Errorf("expected Status=2 (Error/InsufficientCount), got %d", resp.Status)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Action=3 (Store)
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleOpStashInteract_Store(t *testing.T) {
	s, sink, db := newTestServer(t)

	stashID := uint32(33)
	if err := db.SaveStash(stashID, "l03_agroprom", 0, 0, 0, "[]"); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9005}
	raw := buildStashInteractPacket(t, stashID, 3 /*Store*/, "medkit", 2)
	s.HandlePacket(raw, addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	resp := lastStashResponse(t, sink.sent)
	if resp.Status != 0 {
		t.Errorf("Store: expected Status=0, got %d", resp.Status)
	}

	mgr := s.stashMgr
	stash, err := mgr.GetStash(stashID)
	if err != nil {
		t.Fatalf("GetStash: %v", err)
	}
	var gotItems []StashItem
	_ = json.Unmarshal(stash.Contents, &gotItems)
	if len(gotItems) != 1 || gotItems[0].Section != "medkit" || gotItems[0].Count != 2 {
		t.Errorf("expected 2 medkit after store, got %+v", gotItems)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Unknown action
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleOpStashInteract_UnknownAction(t *testing.T) {
	s, sink, _ := newTestServer(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9006}
	raw := buildStashInteractPacket(t, 1, 99 /*unknown*/, "", 0)
	s.HandlePacket(raw, addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	resp := lastStashResponse(t, sink.sent)
	if resp.Status != 2 {
		t.Errorf("expected Status=2 (Error) for unknown action, got %d", resp.Status)
	}
}
