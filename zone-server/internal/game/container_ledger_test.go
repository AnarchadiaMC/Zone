package game

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"sync"
	"testing"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

func sendContainerAction(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, pkt protocol.ContainerActionPacket) {
	t.Helper()
	raw := buildTestPacket(t, protocol.OpContainerAction, seq, protocol.FlagReliable, pkt)
	s.HandlePacket(raw, sess.UDPAddr)
}

func containerUpdatesByResult(t *testing.T, sink *fakeSink) map[uint8][]protocol.ContainerUpdatePacket {
	t.Helper()
	out := map[uint8][]protocol.ContainerUpdatePacket{}
	for _, raw := range packetsByOpcode(sink, protocol.OpContainerUpdate) {
		var u protocol.ContainerUpdatePacket
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &u); err == nil {
			out[u.Result] = append(out[u.Result], u)
		}
	}
	return out
}

func stashItems(t *testing.T, s *Server, stashID uint32) []StashItem {
	t.Helper()
	rec, err := s.db.GetStash(stashID)
	if err != nil {
		t.Fatalf("GetStash(%d): %v", stashID, err)
	}
	var items []StashItem
	if err := json.Unmarshal([]byte(rec.ContentsJSON), &items); err != nil {
		t.Fatalf("stash %d contents %q: %v", stashID, rec.ContentsJSON, err)
	}
	return items
}

func countAuditEvents(t *testing.T, s *Server, eventType string) int {
	t.Helper()
	var n int
	if err := s.db.RawDB().QueryRow("SELECT COUNT(*) FROM audit_log WHERE event_type=?", eventType).Scan(&n); err != nil {
		t.Fatalf("audit count %s: %v", eventType, err)
	}
	return n
}

func TestContainerDepositWithdrawRoundTrip(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-container", "hwid-container", "Container"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-container", "bandage", 5, 1.0)
	if err := db.SaveStash(300, "l01_escape", 10, 0, 10, "[]"); err != nil {
		t.Fatalf("SaveStash: %v", err)
	}
	sess := ledgerSession(t, s, 7001, 45001, "uuid-container", "l01_escape", [3]float32{10, 0, 10})
	sink.Reset()

	deposit := protocol.ContainerActionPacket{
		ActionID:    1,
		ContainerID: 300,
		Action:      protocol.ContainerActionDeposit,
		Count:       2,
		Condition:   80,
	}
	assembleSection(&deposit.Section, "bandage")
	sendContainerAction(t, s, sess, 1, deposit)

	updates := containerUpdatesByResult(t, sink)
	ok := updates[protocol.ItemResultOK]
	if len(ok) != 1 {
		t.Fatalf("deposit OK updates = %d, want 1 (all: %+v)", len(ok), updates)
	}
	if ok[0].Count != -2 || ok[0].Action != protocol.ContainerActionDeposit || ok[0].ContainerID != 300 {
		t.Fatalf("deposit update = %+v, want count=-2 action=deposit container=300", ok[0])
	}
	if avail, _ := db.AvailableItemCount("uuid-container", "bandage"); avail != 3 {
		t.Fatalf("inventory after deposit = %d, want 3", avail)
	}
	items := stashItems(t, s, 300)
	if len(items) != 1 || items[0].Section != "bandage" || items[0].Count != 2 || items[0].Condition != 80 {
		t.Fatalf("stash after deposit = %+v", items)
	}

	sink.Reset()
	withdraw := protocol.ContainerActionPacket{
		ActionID:    2,
		ContainerID: 300,
		Action:      protocol.ContainerActionWithdraw,
		Count:       1,
		Condition:   80,
	}
	assembleSection(&withdraw.Section, "bandage")
	sendContainerAction(t, s, sess, 2, withdraw)

	updates = containerUpdatesByResult(t, sink)
	ok = updates[protocol.ItemResultOK]
	if len(ok) != 1 {
		t.Fatalf("withdraw OK updates = %d, want 1 (all: %+v)", len(ok), updates)
	}
	if ok[0].Count != 1 || ok[0].Action != protocol.ContainerActionWithdraw {
		t.Fatalf("withdraw update = %+v, want count=+1 action=withdraw", ok[0])
	}
	if avail, _ := db.AvailableItemCount("uuid-container", "bandage"); avail != 4 {
		t.Fatalf("inventory after withdraw = %d, want 4", avail)
	}
	items = stashItems(t, s, 300)
	if len(items) != 1 || items[0].Count != 1 {
		t.Fatalf("stash after withdraw = %+v", items)
	}

	if got := countAuditEvents(t, s, "item_deposit"); got != 1 {
		t.Errorf("item_deposit audit rows = %d, want 1", got)
	}
	if got := countAuditEvents(t, s, "item_withdraw"); got != 1 {
		t.Errorf("item_withdraw audit rows = %d, want 1", got)
	}
}

// Deposit merges by section + condition bucket, keeping separate stacks per
// condition.
func TestContainerDepositConditionBuckets(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-buckets", "hwid-buckets", "Buckets"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-buckets", "bandage", 6, 1.0)
	if err := db.SaveStash(301, "l01_escape", 0, 0, 0, "[]"); err != nil {
		t.Fatalf("SaveStash: %v", err)
	}
	sess := ledgerSession(t, s, 7002, 45002, "uuid-buckets", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	deposit := func(actionID uint32, count uint16, condition uint8) {
		pkt := protocol.ContainerActionPacket{
			ActionID: actionID, ContainerID: 301, Action: protocol.ContainerActionDeposit,
			Count: count, Condition: condition,
		}
		assembleSection(&pkt.Section, "bandage")
		sendContainerAction(t, s, sess, actionID, pkt)
	}
	deposit(1, 1, 80)
	deposit(2, 2, 20)
	deposit(3, 3, 80)

	items := stashItems(t, s, 301)
	byCond := map[uint8]int{}
	for _, it := range items {
		byCond[it.Condition] += it.Count
	}
	if len(items) != 2 || byCond[80] != 4 || byCond[20] != 2 {
		t.Fatalf("bucket merge = %+v, want 80:4 20:2", items)
	}
}

func TestContainerDepositInsufficientRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-short", "hwid-short", "Short"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-short", "bandage", 1, 1.0)
	if err := db.SaveStash(302, "l01_escape", 0, 0, 0, "[]"); err != nil {
		t.Fatalf("SaveStash: %v", err)
	}
	sess := ledgerSession(t, s, 7003, 45003, "uuid-short", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	pkt := protocol.ContainerActionPacket{
		ActionID: 1, ContainerID: 302, Action: protocol.ContainerActionDeposit,
		Count: 5, Condition: 100,
	}
	assembleSection(&pkt.Section, "bandage")
	sendContainerAction(t, s, sess, 1, pkt)

	updates := containerUpdatesByResult(t, sink)
	corrected := updates[protocol.ItemResultCorrected]
	if len(corrected) != 1 {
		t.Fatalf("corrected updates = %d, want 1 (all: %+v)", len(corrected), updates)
	}
	if corrected[0].Count != -1 {
		t.Fatalf("correction count = %d, want -1 (server holds 1)", corrected[0].Count)
	}
	if len(packetsByOpcode(sink, protocol.OpInventorySync)) == 0 {
		t.Fatal("correction did not force an inventory sync")
	}
	if items := stashItems(t, s, 302); len(items) != 0 {
		t.Fatalf("stash changed on rejected deposit: %+v", items)
	}
	if avail, _ := db.AvailableItemCount("uuid-short", "bandage"); avail != 1 {
		t.Fatalf("inventory changed on rejected deposit: %d, want 1", avail)
	}
	if got := countAuditEvents(t, s, "item_deposit_rejected"); got != 1 {
		t.Errorf("item_deposit_rejected audit rows = %d, want 1", got)
	}
}

func TestContainerOutOfRangeAndWrongLevelRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-gate", "hwid-gate", "Gate"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-gate", "bandage", 5, 1.0)
	if err := db.SaveStash(303, "l01_escape", 100, 0, 100, "[]"); err != nil {
		t.Fatalf("SaveStash far: %v", err)
	}
	if err := db.SaveStash(304, "l02_garbage", 0, 0, 0, "[]"); err != nil {
		t.Fatalf("SaveStash wrong level: %v", err)
	}
	sess := ledgerSession(t, s, 7004, 45004, "uuid-gate", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	deposit := func(actionID, containerID uint32) {
		pkt := protocol.ContainerActionPacket{
			ActionID: actionID, ContainerID: containerID, Action: protocol.ContainerActionDeposit,
			Count: 1, Condition: 100,
		}
		assembleSection(&pkt.Section, "bandage")
		sendContainerAction(t, s, sess, actionID, pkt)
	}
	deposit(1, 303) // out of range
	deposit(2, 304) // wrong level

	updates := containerUpdatesByResult(t, sink)
	if len(updates[protocol.ItemResultRejected]) != 2 {
		t.Fatalf("rejected updates = %d, want 2 (all: %+v)", len(updates[protocol.ItemResultRejected]), updates)
	}
	if avail, _ := db.AvailableItemCount("uuid-gate", "bandage"); avail != 5 {
		t.Fatalf("inventory changed on rejected deposits: %d", avail)
	}
	if got := countAuditEvents(t, s, "item_container_rejected"); got < 2 {
		t.Errorf("item_container_rejected audit rows = %d, want >= 2", got)
	}
}

// The item and container ledgers share one ActionID floor and one rate window:
// a replayed 0x007F echoes its cached OpContainerUpdate, and reusing the ID for
// a 0x007D drop is rejected as stale.
func TestContainerReplaySharesItemLedgerCache(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-shared", "hwid-shared", "Shared"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-shared", "bandage", 5, 1.0)
	if err := db.SaveStash(305, "l01_escape", 0, 0, 0, "[]"); err != nil {
		t.Fatalf("SaveStash: %v", err)
	}
	sess := ledgerSession(t, s, 7005, 45005, "uuid-shared", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	deposit := protocol.ContainerActionPacket{
		ActionID: 7, ContainerID: 305, Action: protocol.ContainerActionDeposit,
		Count: 2, Condition: 100,
	}
	assembleSection(&deposit.Section, "bandage")
	sendContainerAction(t, s, sess, 1, deposit)
	if got := containerUpdatesByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("deposit OK updates = %d, want 1", len(got))
	}

	// Replay under a new sequence: cached echo, no re-apply.
	sink.Reset()
	sendContainerAction(t, s, sess, 2, deposit)
	if got := containerUpdatesByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("replayed deposit OK updates = %d, want 1", len(got))
	}
	if items := stashItems(t, s, 305); len(items) != 1 || items[0].Count != 2 {
		t.Fatalf("replay changed stash: %+v", items)
	}
	if avail, _ := db.AvailableItemCount("uuid-shared", "bandage"); avail != 3 {
		t.Fatalf("replay changed inventory: %d, want 3", avail)
	}

	// Same ActionID through the item opcode must hit the shared floor and be
	// rejected as stale (it is not in the item result cache).
	sink.Reset()
	var drop protocol.ItemActionPacket
	drop.ActionID = 7
	drop.Action = protocol.ItemActionDrop
	drop.Count = 1
	assembleSection(&drop.Section, "bandage")
	sendItemAction(t, s, sess, 3, drop)
	if got := itemUpdatesByResult(t, sink)[protocol.ItemResultRejected]; len(got) != 1 {
		t.Fatalf("cross-opcode reuse not rejected: %d", len(got))
	}
	if got := countWorldItems(t, s); got != 0 {
		t.Fatalf("cross-opcode reuse created world items: %d", got)
	}

	// A fresh ActionID applies normally.
	sink.Reset()
	deposit.ActionID = 8
	sendContainerAction(t, s, sess, 4, deposit)
	if got := containerUpdatesByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("fresh deposit OK updates = %d, want 1", len(got))
	}
	if items := stashItems(t, s, 305); len(items) != 1 || items[0].Count != 4 {
		t.Fatalf("fresh deposit stash = %+v, want count 4", items)
	}
}

// Two sessions withdrawing the same last copy: exactly one wins.
func TestContainerConcurrentWithdrawSingleWinner(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-wa", "hwid-wa", "WinnerA"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.AutoProvision("uuid-wb", "hwid-wb", "WinnerB"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	contents, _ := json.Marshal([]StashItem{{Section: "medkit", Count: 1, Condition: 100}})
	if err := db.SaveStash(306, "l01_escape", 0, 0, 0, string(contents)); err != nil {
		t.Fatalf("SaveStash: %v", err)
	}
	sessA := ledgerSession(t, s, 7006, 45006, "uuid-wa", "l01_escape", [3]float32{0, 0, 0})
	sessB := ledgerSession(t, s, 7007, 45007, "uuid-wb", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	var wg sync.WaitGroup
	for _, sess := range []*network.PlayerSession{sessA, sessB} {
		wg.Add(1)
		go func(sess *network.PlayerSession) {
			defer wg.Done()
			pkt := protocol.ContainerActionPacket{
				ActionID: 1, ContainerID: 306, Action: protocol.ContainerActionWithdraw,
				Count: 1, Condition: 100,
			}
			assembleSection(&pkt.Section, "medkit")
			raw := buildTestPacket(t, protocol.OpContainerAction, 1, protocol.FlagReliable, pkt)
			s.HandlePacket(raw, sess.UDPAddr)
		}(sess)
	}
	wg.Wait()

	updates := containerUpdatesByResult(t, sink)
	if len(updates[protocol.ItemResultOK]) != 1 {
		t.Fatalf("withdraw winners = %d, want 1 (all: %+v)", len(updates[protocol.ItemResultOK]), updates)
	}
	if len(updates[protocol.ItemResultRejected]) != 1 {
		t.Fatalf("withdraw losers = %d, want 1 (all: %+v)", len(updates[protocol.ItemResultRejected]), updates)
	}
	if items := stashItems(t, s, 306); len(items) != 0 {
		t.Fatalf("stash after race = %+v, want empty", items)
	}
	totalA, _ := db.AvailableItemCount("uuid-wa", "medkit")
	totalB, _ := db.AvailableItemCount("uuid-wb", "medkit")
	if totalA+totalB != 1 {
		t.Fatalf("credited medkits = %d, want exactly 1", totalA+totalB)
	}
}

func TestContainerRateLimit(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-crate", "hwid-crate", "Crate"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-crate", "bandage", 10, 1.0)
	if err := db.SaveStash(307, "l01_escape", 0, 0, 0, "[]"); err != nil {
		t.Fatalf("SaveStash: %v", err)
	}
	sess := ledgerSession(t, s, 7008, 45008, "uuid-crate", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	for i := 0; i < 6; i++ {
		pkt := protocol.ContainerActionPacket{
			ActionID: uint32(i + 1), ContainerID: 307, Action: protocol.ContainerActionDeposit,
			Count: 1, Condition: 100,
		}
		assembleSection(&pkt.Section, "bandage")
		sendContainerAction(t, s, sess, uint32(i+1), pkt)
	}

	updates := containerUpdatesByResult(t, sink)
	if got := len(updates[protocol.ItemResultOK]); got != 5 {
		t.Fatalf("accepted deposits = %d, want 5 (rate limit item_rate_per_s=5)", got)
	}
	if got := len(updates[protocol.ItemResultRejected]); got != 1 {
		t.Fatalf("rate-limited rejections = %d, want 1", got)
	}
	if items := stashItems(t, s, 307); len(items) != 1 || items[0].Count != 5 {
		t.Fatalf("stash after rate limit = %+v, want count 5", items)
	}
}

// Consumption (0x007D action=3) removes exactly once, echoes a negative delta
// to the sender only, and corrects diverged clients.
func TestConsumeRemovesExactlyOnce(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-eat", "hwid-eat", "Eater"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-eat", "bandage", 3, 1.0)
	sess := ledgerSession(t, s, 7009, 45009, "uuid-eat", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	var pkt protocol.ItemActionPacket
	pkt.ActionID = 1
	pkt.Action = protocol.ItemActionConsume
	pkt.Count = 2
	pkt.Condition = 100
	assembleSection(&pkt.Section, "bandage")
	sendItemAction(t, s, sess, 1, pkt)

	updates := itemUpdatesByResult(t, sink)
	ok := updates[protocol.ItemResultOK]
	if len(ok) != 1 {
		t.Fatalf("consume OK updates = %d, want 1 (all: %+v)", len(ok), updates)
	}
	if ok[0].Action != protocol.ItemActionConsume || ok[0].Count != -2 {
		t.Fatalf("consume update = %+v, want action=3 count=-2", ok[0])
	}
	if avail, _ := db.AvailableItemCount("uuid-eat", "bandage"); avail != 1 {
		t.Fatalf("inventory after consume = %d, want 1", avail)
	}

	// Replay under a new sequence: echo without removing again.
	sink.Reset()
	sendItemAction(t, s, sess, 2, pkt)
	if got := itemUpdatesByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("replayed consume updates = %d, want 1", len(got))
	}
	if avail, _ := db.AvailableItemCount("uuid-eat", "bandage"); avail != 1 {
		t.Fatalf("replay removed items: %d, want 1", avail)
	}

	// Over-consume is corrected to the authoritative remaining count plus a
	// forced inventory sync.
	sink.Reset()
	pkt.ActionID = 3
	pkt.Count = 5
	sendItemAction(t, s, sess, 3, pkt)
	corrected := itemUpdatesByResult(t, sink)[protocol.ItemResultCorrected]
	if len(corrected) != 1 || corrected[0].Count != -1 {
		t.Fatalf("consume correction = %+v, want count=-1", corrected)
	}
	if len(packetsByOpcode(sink, protocol.OpInventorySync)) == 0 {
		t.Fatal("consume correction did not force an inventory sync")
	}
	if got := countAuditEvents(t, s, "item_consume"); got != 1 {
		t.Errorf("item_consume audit rows = %d, want 1", got)
	}
	if got := countAuditEvents(t, s, "item_consume_rejected"); got != 1 {
		t.Errorf("item_consume_rejected audit rows = %d, want 1", got)
	}
}
