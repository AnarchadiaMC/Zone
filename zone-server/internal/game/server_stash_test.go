package game

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"

	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"

	"go.uber.org/zap"
)

// stashTestSeq provides unique increasing sequence numbers for stash test
// packets. Required since the server now enforces per-session replay
// protection (hdr.SequenceNum <= LastSequence is dropped): reusing a fixed
// seq (e.g. two Takes with seq 1) would be treated as a replay, not a new
// request.
var stashTestSeq atomic.Uint32

// buildStashInteractPacket creates a fully-framed OpStashInteract UDP payload
// with a unique wire sequence number (see stashTestSeq).
func buildStashInteractPacket(t *testing.T, stashID uint32, action uint8, section string, count uint16) []byte {
	t.Helper()
	return buildStashInteractPacketSeq(t, stashID, action, section, count, stashTestSeq.Add(1))
}

// buildStashInteractPacketSeq creates a fully-framed OpStashInteract UDP
// payload with an explicit sequence number. Replay protection drops reliable
// packets with seq <= the session's last seen seq, so consecutive sends in
// one test must use increasing seq numbers.
func buildStashInteractPacketSeq(t *testing.T, stashID uint32, action uint8, section string, count uint16, seq uint32) []byte {
	t.Helper()

	pkt := protocol.StashInteractPayload{
		StashID: stashID,
		Action:  action,
		Count:   count,
	}
	copy(pkt.ItemSection[:], section)

	var buf bytes.Buffer
	if err := protocol.WritePacket(&buf, protocol.OpStashInteract, seq, protocol.FlagReliable, pkt); err != nil {
		t.Fatalf("buildStashInteractPacketSeq: WritePacket failed: %v", err)
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
		db:       db,
		sessions: newFakeSessionManager(),
		stashMgr: NewStashManager(db),
		logger:   logger,
	}

	sink := &fakeSink{}
	s.udp = sink.UDPListener()

	return s, sink, db
}

// registerStashTestSession registers a player session for addr so stash
// access checks (unknown-session / level / distance) can succeed.
func registerStashTestSession(t *testing.T, s *Server, addr *net.UDPAddr, level string, pos [3]float32) {
	t.Helper()
	s.sessions.AddSession(&network.PlayerSession{
		SessionID:    uint32(addr.Port),
		AccountID:    "test-uuid-" + addr.String(),
		UDPAddr:      addr,
		CurrentLevel: level,
		Position:     pos,
		Health:       100,
	})
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
	registerStashTestSession(t, s, addr, "l01_escape", [3]float32{1.0, 2.0, 3.0})
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
	registerStashTestSession(t, s, addr, "l01_escape", [3]float32{0, 0, 0})
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
	registerStashTestSession(t, s, addr, "l02_garbage", [3]float32{0, 0, 0})
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
	registerStashTestSession(t, s, addr, "l01_escape", [3]float32{0, 0, 0})
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
	registerStashTestSession(t, s, addr, "l03_agroprom", [3]float32{0, 0, 0})
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
	registerStashTestSession(t, s, addr, "l01_escape", [3]float32{0, 0, 0})
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

// ─────────────────────────────────────────────────────────────────────────────
// Security: distance / passcode / unknown-session / no-duplication
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleOpStashInteract_UnknownSessionRejected(t *testing.T) {
	s, sink, db := newTestServer(t)

	stashID := uint32(44)
	itemsJSON, _ := json.Marshal([]StashItem{{Section: "medkit", Count: 1}})
	if err := db.SaveStash(stashID, "l01_escape", 0, 0, 0, string(itemsJSON)); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	// No session registered for this addr — must get Status=2, not a silent drop.
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9010}
	s.HandlePacket(buildStashInteractPacket(t, stashID, 1, "", 0), addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet for unknown session, got none")
	}
	if resp := lastStashResponse(t, sink.sent); resp.Status != 2 {
		t.Errorf("expected Status=2 (Error) for unknown session, got %d", resp.Status)
	}
}

func TestHandleOpStashInteract_Take_TooFarRejected(t *testing.T) {
	s, sink, db := newTestServer(t)

	stashID := uint32(45)
	itemsJSON, _ := json.Marshal([]StashItem{{Section: "ammo_9x18_fmj", Count: 10}})
	if err := db.SaveStash(stashID, "l01_escape", 0, 0, 0, string(itemsJSON)); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9011}
	// 200m away on the same level — beyond the 5m interact radius.
	registerStashTestSession(t, s, addr, "l01_escape", [3]float32{200, 0, 0})
	s.HandlePacket(buildStashInteractPacket(t, stashID, 2, "ammo_9x18_fmj", 1), addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	if resp := lastStashResponse(t, sink.sent); resp.Status != 2 {
		t.Errorf("expected Status=2 (Error) for 200m Take, got %d", resp.Status)
	}

	// Contents must be untouched.
	stash, err := s.stashMgr.GetStash(stashID)
	if err != nil {
		t.Fatalf("GetStash: %v", err)
	}
	var gotItems []StashItem
	_ = json.Unmarshal(stash.Contents, &gotItems)
	if len(gotItems) != 1 || gotItems[0].Count != 10 {
		t.Errorf("expected untouched 10 ammo after rejected Take, got %+v", gotItems)
	}
}

func TestHandleOpStashInteract_Take_WrongLevelRejected(t *testing.T) {
	s, sink, db := newTestServer(t)

	stashID := uint32(46)
	itemsJSON, _ := json.Marshal([]StashItem{{Section: "bandage", Count: 2}})
	if err := db.SaveStash(stashID, "l01_escape", 0, 0, 0, string(itemsJSON)); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9012}
	// Same position but different level.
	registerStashTestSession(t, s, addr, "l02_garbage", [3]float32{0, 0, 0})
	s.HandlePacket(buildStashInteractPacket(t, stashID, 2, "bandage", 1), addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	if resp := lastStashResponse(t, sink.sent); resp.Status != 2 {
		t.Errorf("expected Status=2 (Error) for wrong-level Take, got %d", resp.Status)
	}
}

func TestHandleOpStashInteract_WrongPasscodeRejected(t *testing.T) {
	s, sink, db := newTestServer(t)

	stashID := uint32(47)
	itemsJSON, _ := json.Marshal([]StashItem{{Section: "medkit", Count: 2}})
	if err := db.SaveStash(stashID, "l01_escape", 5, 0, 5, string(itemsJSON)); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}
	// SaveStash has no passcode param — set it directly.
	if _, err := db.RawDB().Exec(`UPDATE world_stashes SET passcode = ? WHERE stash_id = ?`, "s3cret", stashID); err != nil {
		t.Fatalf("failed to set passcode: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9013}
	registerStashTestSession(t, s, addr, "l01_escape", [3]float32{5, 0, 5})
	// Wire protocol carries no passcode field, so the request authenticates
	// with "" and must be rejected for a passcode-protected stash.
	s.HandlePacket(buildStashInteractPacket(t, stashID, 2, "medkit", 1), addr)

	if len(sink.sent) == 0 {
		t.Fatal("expected a response packet, got none")
	}
	if resp := lastStashResponse(t, sink.sent); resp.Status != 2 {
		t.Errorf("expected Status=2 (Error) for wrong passcode, got %d", resp.Status)
	}

	// Direct ValidateAccess sanity: correct passcode passes, wrong fails.
	rec, err := db.GetStash(stashID)
	if err != nil {
		t.Fatalf("GetStash: %v", err)
	}
	if err := s.stashMgr.ValidateAccess(rec, [3]float32{5, 0, 5}, "l01_escape", "s3cret"); err != nil {
		t.Errorf("ValidateAccess with correct passcode should pass, got %v", err)
	}
	if err := s.stashMgr.ValidateAccess(rec, [3]float32{5, 0, 5}, "l01_escape", "wrong"); err == nil {
		t.Errorf("ValidateAccess with wrong passcode should fail")
	}
}

func TestHandleOpStashInteract_Take_NoDuplication(t *testing.T) {
	s, sink, db := newTestServer(t)

	stashID := uint32(48)
	itemsJSON, _ := json.Marshal([]StashItem{{Section: "bandage", Count: 1}})
	if err := db.SaveStash(stashID, "l01_escape", 0, 0, 0, string(itemsJSON)); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9014}
	registerStashTestSession(t, s, addr, "l01_escape", [3]float32{0, 0, 0})

	// First Take of 1 succeeds.
	s.HandlePacket(buildStashInteractPacket(t, stashID, 2, "bandage", 1), addr)
	if resp := lastStashResponse(t, sink.sent); resp.Status != 0 {
		t.Fatalf("first Take: expected Status=0, got %d", resp.Status)
	}

	// Second Take of 1 must fail — no duplication, no negative counts.
	sink.Reset()
	// Re-arm ACK accounting: HandlePacket sends an OpAck before the response,
	// lastStashResponse skips it, so no extra setup needed.
	// NOTE: seq 2 — replay protection drops reliable re-sends of seq 1.
	s.HandlePacket(buildStashInteractPacket(t, stashID, 2, "bandage", 1), addr)
	if resp := lastStashResponse(t, sink.sent); resp.Status != 2 {
		t.Errorf("second Take: expected Status=2 (Error), got %d", resp.Status)
	}

	stash, err := s.stashMgr.GetStash(stashID)
	if err != nil {
		t.Fatalf("GetStash: %v", err)
	}
	var gotItems []StashItem
	_ = json.Unmarshal(stash.Contents, &gotItems)
	for _, it := range gotItems {
		if it.Count < 0 {
			t.Errorf("negative count detected: %+v", gotItems)
		}
		if it.Section == "bandage" {
			t.Errorf("expected bandage fully removed, got %+v", gotItems)
		}
	}
}
