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

// ledgerSession inserts a connected player into the server for item tests.
func ledgerSession(t *testing.T, s *Server, id uint32, port int, uuid, level string, pos [3]float32) *network.PlayerSession {
	t.Helper()
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	sess := &network.PlayerSession{
		SessionID:    id,
		AccountID:    uuid,
		UDPAddr:      addr,
		CurrentLevel: level,
		Position:     pos,
		Health:       100,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)
	return sess
}

func sendItemAction(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, pkt protocol.ItemActionPacket) {
	t.Helper()
	raw := buildTestPacket(t, protocol.OpItemAction, seq, protocol.FlagReliable, pkt)
	s.HandlePacket(raw, sess.UDPAddr)
}

func itemUpdatesByResult(t *testing.T, sink *fakeSink) map[uint8][]protocol.ItemUpdatePacket {
	t.Helper()
	out := map[uint8][]protocol.ItemUpdatePacket{}
	for _, raw := range packetsByOpcode(sink, protocol.OpItemUpdate) {
		var u protocol.ItemUpdatePacket
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &u); err == nil {
			out[u.Result] = append(out[u.Result], u)
		}
	}
	return out
}

func mustExec(t *testing.T, s *Server, query string, args ...interface{}) {
	t.Helper()
	if _, err := s.db.RawDB().Exec(query, args...); err != nil {
		t.Fatalf("exec %q failed: %v", query, err)
	}
}

func countWorldItems(t *testing.T, s *Server) int {
	t.Helper()
	var n int
	if err := s.db.RawDB().QueryRow("SELECT COUNT(*) FROM world_items").Scan(&n); err != nil {
		t.Fatalf("world_items count failed: %v", err)
	}
	return n
}

func assembleSection(dst *[64]byte, s string) {
	copy(dst[:], s)
}

func TestItemDropDecrementsInventoryAndBroadcasts(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-dropper", "hwid-dropper", "Dropper"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-dropper", "bandage", 3, 0.8)

	dropper := ledgerSession(t, s, 6101, 44001, "uuid-dropper", "l01_escape", [3]float32{10, 0, 10})
	peer := ledgerSession(t, s, 6102, 44002, "uuid-peer", "l01_escape", [3]float32{12, 0, 10})
	_ = peer
	sink.Reset()

	var pkt protocol.ItemActionPacket
	pkt.ActionID = 1
	pkt.Action = protocol.ItemActionDrop
	pkt.Count = 2
	pkt.X, pkt.Y, pkt.Z = 11.5, 0.5, 9.5
	pkt.Condition = 80
	assembleSection(&pkt.Section, "bandage")
	sendItemAction(t, s, dropper, 1, pkt)

	updates := itemUpdatesByResult(t, sink)
	okUpdates := updates[protocol.ItemResultOK]
	if len(okUpdates) != 2 {
		t.Fatalf("expected dropper+peer to receive OK update, got %d (all: %+v)", len(okUpdates), updates)
	}
	first := okUpdates[0]
	if first.ActionID != 1 {
		t.Fatalf("ActionID echo = %d, want 1", first.ActionID)
	}
	if first.ItemID == 0 || first.Count != -2 || first.Action != protocol.ItemActionDrop {
		t.Fatalf("drop update = %+v, want ItemID>0 Count=-2 Action=drop", first)
	}

	avail, err := db.AvailableItemCount("uuid-dropper", "bandage")
	if err != nil || avail != 1 {
		t.Fatalf("inventory bandage = %d err=%v, want 1", avail, err)
	}
	if got := countWorldItems(t, s); got != 1 {
		t.Fatalf("world_items rows = %d, want 1", got)
	}
	wi, err := db.GetWorldItem(int64(first.ItemID))
	if err != nil {
		t.Fatalf("GetWorldItem: %v", err)
	}
	if wi.Count != 2 || wi.Section != "bandage" || wi.LevelName != "l01_escape" {
		t.Fatalf("world item = %+v", wi)
	}
}

func TestItemReplayActionIDEchoesWithoutDuplicate(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-replay", "hwid-replay", "Replay"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-replay", "bandage", 2, 1.0)
	sess := ledgerSession(t, s, 6201, 44011, "uuid-replay", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	var pkt protocol.ItemActionPacket
	pkt.ActionID = 7
	pkt.Action = protocol.ItemActionDrop
	pkt.Count = 1
	assembleSection(&pkt.Section, "bandage")
	sendItemAction(t, s, sess, 1, pkt)
	if got := countWorldItems(t, s); got != 1 {
		t.Fatalf("world items after first drop = %d, want 1", got)
	}
	if avail, _ := db.AvailableItemCount("uuid-replay", "bandage"); avail != 1 {
		t.Fatalf("inventory after first drop = %d, want 1", avail)
	}
	sink.Reset()

	// Same ActionID retransmitted under a new sequence (so the generic replay
	// gate lets it through): must echo the cached result, not drop again.
	sendItemAction(t, s, sess, 2, pkt)

	updates := itemUpdatesByResult(t, sink)
	if len(updates[protocol.ItemResultOK]) != 1 {
		t.Fatalf("replay did not echo exactly one OK update: %+v", updates)
	}
	if got := countWorldItems(t, s); got != 1 {
		t.Fatalf("replay duplicated world item: rows = %d, want 1", got)
	}
	if avail, _ := db.AvailableItemCount("uuid-replay", "bandage"); avail != 1 {
		t.Fatalf("replay changed inventory: %d, want 1", avail)
	}
}

func TestItemPickupDeletesRowAndCreditsOnce(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-picker", "hwid-picker", "Picker"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section, item_count, condition) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"l01_escape", 5, 1, 5, "medkit", 2, 0.5)
	var itemID int64
	if err := db.RawDB().QueryRow("SELECT id FROM world_items LIMIT 1").Scan(&itemID); err != nil {
		t.Fatalf("world item id: %v", err)
	}

	picker := ledgerSession(t, s, 6301, 44021, "uuid-picker", "l01_escape", [3]float32{5, 1, 5})
	sink.Reset()

	var pkt protocol.ItemActionPacket
	pkt.ActionID = 1
	pkt.Action = protocol.ItemActionPickup
	pkt.ItemID = uint32(itemID)
	assembleSection(&pkt.Section, "forged_section_ignored")
	sendItemAction(t, s, picker, 1, pkt)

	updates := itemUpdatesByResult(t, sink)
	okUpdates := updates[protocol.ItemResultOK]
	if len(okUpdates) != 1 {
		t.Fatalf("expected 1 OK pickup update, got %+v", updates)
	}
	up := okUpdates[0]
	if up.Count != 2 || up.ItemID != uint32(itemID) {
		t.Fatalf("pickup update = %+v, want Count=2 ItemID=%d", up, itemID)
	}
	if got := string(bytes.TrimRight(up.Section[:], "\x00")); got != "medkit" {
		t.Fatalf("pickup section = %q, want DB value medkit", got)
	}
	if got := countWorldItems(t, s); got != 0 {
		t.Fatalf("world items after pickup = %d, want 0", got)
	}
	if avail, _ := db.AvailableItemCount("uuid-picker", "medkit"); avail != 2 {
		t.Fatalf("credited medkit = %d, want 2", avail)
	}

	// Replayed/duplicated pickup of the same world id must be rejected and
	// must not credit again.
	pkt.ActionID = 2
	sink.Reset()
	sendItemAction(t, s, picker, 2, pkt)
	rejected := itemUpdatesByResult(t, sink)[protocol.ItemResultRejected]
	if len(rejected) != 1 {
		t.Fatalf("second pickup not rejected: %+v", itemUpdatesByResult(t, sink))
	}
	if avail, _ := db.AvailableItemCount("uuid-picker", "medkit"); avail != 2 {
		t.Fatalf("double pickup credited twice: %d, want 2", avail)
	}
}

func TestItemPickupOutOfRangeRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-far", "hwid-far", "Far"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section, item_count, condition) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"l01_escape", 100, 0, 100, "medkit", 1, 1.0)
	var itemID int64
	if err := db.RawDB().QueryRow("SELECT id FROM world_items LIMIT 1").Scan(&itemID); err != nil {
		t.Fatalf("world item id: %v", err)
	}
	picker := ledgerSession(t, s, 6401, 44031, "uuid-far", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	var pkt protocol.ItemActionPacket
	pkt.ActionID = 1
	pkt.Action = protocol.ItemActionPickup
	pkt.ItemID = uint32(itemID)
	sendItemAction(t, s, picker, 1, pkt)

	updates := itemUpdatesByResult(t, sink)
	if len(updates[protocol.ItemResultRejected]) != 1 {
		t.Fatalf("out-of-range pickup not rejected: %+v", updates)
	}
	if got := countWorldItems(t, s); got != 1 {
		t.Fatalf("out-of-range pickup removed the item: rows = %d", got)
	}
}

func TestItemPickupWrongLevelRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-wrong-level", "hwid-wrong-level", "WrongLevel"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section, item_count, condition) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"l02_garbage", 0, 0, 0, "medkit", 1, 1.0)
	var itemID int64
	if err := db.RawDB().QueryRow("SELECT id FROM world_items LIMIT 1").Scan(&itemID); err != nil {
		t.Fatalf("world item id: %v", err)
	}
	picker := ledgerSession(t, s, 6451, 44041, "uuid-wrong-level", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	var pkt protocol.ItemActionPacket
	pkt.ActionID = 1
	pkt.Action = protocol.ItemActionPickup
	pkt.ItemID = uint32(itemID)
	sendItemAction(t, s, picker, 1, pkt)
	if len(itemUpdatesByResult(t, sink)[protocol.ItemResultRejected]) != 1 {
		t.Fatalf("wrong-level pickup not rejected")
	}
}

func TestItemDropCorrectionResyncsInventory(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-correct", "hwid-correct", "Correct"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-correct", "bandage", 1, 1.0)
	sess := ledgerSession(t, s, 6501, 44051, "uuid-correct", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	var pkt protocol.ItemActionPacket
	pkt.ActionID = 1
	pkt.Action = protocol.ItemActionDrop
	pkt.Count = 5
	assembleSection(&pkt.Section, "bandage")
	sendItemAction(t, s, sess, 1, pkt)

	updates := itemUpdatesByResult(t, sink)
	corrected := updates[protocol.ItemResultCorrected]
	if len(corrected) != 1 {
		t.Fatalf("expected 1 corrected update, got %+v", updates)
	}
	if corrected[0].Count != -1 {
		t.Fatalf("correction count = %d, want -1 (server holds 1)", corrected[0].Count)
	}
	if len(packetsByOpcode(sink, protocol.OpInventorySync)) == 0 {
		t.Fatal("correction did not force an inventory sync")
	}
}

func TestItemRateLimitSixthActionRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-rate", "hwid-rate", "Rate"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-rate", "bandage", 10, 1.0)
	sess := ledgerSession(t, s, 6601, 44061, "uuid-rate", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	for i := 0; i < 6; i++ {
		var pkt protocol.ItemActionPacket
		pkt.ActionID = uint32(i + 1)
		pkt.Action = protocol.ItemActionDrop
		pkt.Count = 1
		assembleSection(&pkt.Section, "bandage")
		sendItemAction(t, s, sess, uint32(i+1), pkt)
	}

	updates := itemUpdatesByResult(t, sink)
	if got := len(updates[protocol.ItemResultOK]); got != 5 {
		t.Fatalf("accepted drops = %d, want 5 (rate limit item_rate_per_s=5)", got)
	}
	rejected := updates[protocol.ItemResultRejected]
	if len(rejected) != 1 {
		t.Fatalf("expected exactly one rate-limited rejection, got %d", len(rejected))
	}
	if rejected[0].ActionID != 6 {
		t.Fatalf("rate-limited ActionID = %d, want 6", rejected[0].ActionID)
	}
	if got := countWorldItems(t, s); got != 5 {
		t.Fatalf("world items = %d, want 5", got)
	}
}

func TestItemLedgerOldActionIDRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-old", "hwid-old", "Old"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-old", "bandage", 10, 1.0)
	sess := ledgerSession(t, s, 6701, 44071, "uuid-old", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	send := func(id uint32, seq uint32) {
		var pkt protocol.ItemActionPacket
		pkt.ActionID = id
		pkt.Action = protocol.ItemActionDrop
		pkt.Count = 1
		assembleSection(&pkt.Section, "bandage")
		sendItemAction(t, s, sess, seq, pkt)
	}
	send(10, 1)
	sink.Reset()
	send(9, 2) // below the monotonic floor and not cached
	updates := itemUpdatesByResult(t, sink)
	if len(updates[protocol.ItemResultRejected]) != 1 {
		t.Fatalf("stale ActionID not rejected: %+v", updates)
	}
	if got := countWorldItems(t, s); got != 1 {
		t.Fatalf("stale ActionID applied: world items = %d", got)
	}
}

func BenchmarkItemLedger_LookupRecord(b *testing.B) {
	l := NewItemLedger(5)
	now := time.Now()
	l.AllowAction(1, now) // one representative rate-limit token
	var pkt protocol.ItemUpdatePacket
	pkt.ActionID = 42
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.Record(1, uint32(i+1), pkt)
		_, _ = l.Lookup(1, uint32(i+1))
	}
}
