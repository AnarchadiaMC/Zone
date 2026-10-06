package game

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

func sendStashAction(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, pkt protocol.StashActionPacket) {
	t.Helper()
	raw := buildTestPacket(t, protocol.OpStashAction, seq, protocol.FlagReliable, pkt)
	s.HandlePacket(raw, sess.UDPAddr)
}

func sendTradeAction(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, pkt protocol.TradeActionPacket) {
	t.Helper()
	raw := buildTestPacket(t, protocol.OpTradeAction, seq, protocol.FlagReliable, pkt)
	s.HandlePacket(raw, sess.UDPAddr)
}

func stashResultsByResult(t *testing.T, sink *fakeSink) map[uint8][]protocol.StashResultPacket {
	t.Helper()
	out := map[uint8][]protocol.StashResultPacket{}
	for _, raw := range packetsByOpcode(sink, protocol.OpStashResult) {
		var u protocol.StashResultPacket
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &u); err == nil {
			out[u.Result] = append(out[u.Result], u)
		}
	}
	return out
}

func tradeResultsByResult(t *testing.T, sink *fakeSink) map[uint8][]protocol.TradeResultPacket {
	t.Helper()
	out := map[uint8][]protocol.TradeResultPacket{}
	for _, raw := range packetsByOpcode(sink, protocol.OpTradeResult) {
		var u protocol.TradeResultPacket
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &u); err == nil {
			out[u.Result] = append(out[u.Result], u)
		}
	}
	return out
}

func walletUpdates(t *testing.T, sink *fakeSink) []protocol.WalletUpdatePacket {
	t.Helper()
	var out []protocol.WalletUpdatePacket
	for _, raw := range packetsByOpcode(sink, protocol.OpWalletUpdate) {
		var w protocol.WalletUpdatePacket
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &w); err == nil {
			out = append(out, w)
		}
	}
	return out
}

func lastWalletMoney(t *testing.T, sink *fakeSink) (uint32, bool) {
	t.Helper()
	updates := walletUpdates(t, sink)
	if len(updates) == 0 {
		return 0, false
	}
	return updates[len(updates)-1].Money, true
}

func rublesOf(t *testing.T, s *Server, uuid string) int {
	t.Helper()
	var n int
	if err := s.db.RawDB().QueryRow("SELECT rubles FROM characters WHERE client_uuid=?", uuid).Scan(&n); err != nil {
		t.Fatalf("read rubles(%s): %v", uuid, err)
	}
	return n
}

func stashItemsAtKey(t *testing.T, s *Server, level string, x, y, z float32) []StashItem {
	t.Helper()
	rec, err := s.stashMgr.FindWorldStash(level, x, y, z)
	if err != nil {
		return nil
	}
	var items []StashItem
	if err := json.Unmarshal([]byte(rec.ContentsJSON), &items); err != nil {
		t.Fatalf("stash contents %q: %v", rec.ContentsJSON, err)
	}
	return items
}

// The 0.5 m key grid uses floor(v/0.5 + 0.5) * 0.5 for every sign; the Lua
// sender mirrors exactly this formula on the raw floats it transmits.
func TestRoundWorldStashCoordGrid(t *testing.T) {
	cases := []struct {
		in   float32
		want float32
	}{
		{0, 0},
		{0.24, 0},
		{0.25, 0.5},
		{0.74, 0.5},
		{0.75, 1.0},
		{10.25, 10.5},
		{10.2, 10.0},
		{-0.25, 0},
		{-0.75, -0.5},
		{-10.25, -10.0},
	}
	for _, tc := range cases {
		if got := roundWorldStashCoord(tc.in); got != tc.want {
			t.Errorf("roundWorldStashCoord(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// First store creates an ownerless world stash at the rounded key; a following
// take credits the same bucket back into the inventory.
func TestStashStoreCreatesAndTakeRoundTrip(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-stash", "hwid-stash", "Stash"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-stash", "bandage", 5, 1.0)
	sess := ledgerSession(t, s, 7101, 46001, "uuid-stash", "l01_escape", [3]float32{10.25, 0.25, 10.25})
	sink.Reset()

	store := protocol.StashActionPacket{
		ActionID:  1,
		Action:    protocol.StashActionStore,
		X:         10.25,
		Y:         0.25,
		Z:         10.25,
		Count:     2,
		Condition: 100,
	}
	assembleSection(&store.Section, "bandage")
	sendStashAction(t, s, sess, 1, store)

	ok := stashResultsByResult(t, sink)[protocol.ItemResultOK]
	if len(ok) != 1 {
		t.Fatalf("store OK results = %d, want 1", len(ok))
	}
	if ok[0].Count != -2 || ok[0].Action != protocol.StashActionStore {
		t.Fatalf("store result = %+v, want count=-2 action=store", ok[0])
	}
	if ok[0].Condition != 100 {
		t.Fatalf("store condition = %d, want 100 from the inventory row", ok[0].Condition)
	}
	if avail, _ := db.AvailableItemCount("uuid-stash", "bandage"); avail != 3 {
		t.Fatalf("inventory after store = %d, want 3", avail)
	}
	// Key rounding: 10.25 -> 10.5, 0.25 -> 0.5.
	items := stashItemsAtKey(t, s, "l01_escape", 10.5, 0.5, 10.5)
	if len(items) != 1 || items[0].Section != "bandage" || items[0].Count != 2 || items[0].Condition != 100 {
		t.Fatalf("stash after store = %+v", items)
	}

	sink.Reset()
	take := protocol.StashActionPacket{
		ActionID:  2,
		Action:    protocol.StashActionTake,
		X:         10.25,
		Y:         0.25,
		Z:         10.25,
		Count:     1,
		Condition: 100,
	}
	assembleSection(&take.Section, "bandage")
	sendStashAction(t, s, sess, 2, take)

	ok = stashResultsByResult(t, sink)[protocol.ItemResultOK]
	if len(ok) != 1 {
		t.Fatalf("take OK results = %d, want 1", len(ok))
	}
	if ok[0].Count != 1 || ok[0].Action != protocol.StashActionTake {
		t.Fatalf("take result = %+v, want count=+1 action=take", ok[0])
	}
	if avail, _ := db.AvailableItemCount("uuid-stash", "bandage"); avail != 4 {
		t.Fatalf("inventory after take = %d, want 4", avail)
	}
	items = stashItemsAtKey(t, s, "l01_escape", 10.5, 0.5, 10.5)
	if len(items) != 1 || items[0].Count != 1 {
		t.Fatalf("stash after take = %+v", items)
	}

	if got := countAuditEvents(t, s, "stash_store"); got != 1 {
		t.Errorf("stash_store audit rows = %d, want 1", got)
	}
	if got := countAuditEvents(t, s, "stash_take"); got != 1 {
		t.Errorf("stash_take audit rows = %d, want 1", got)
	}
}

// Store beyond the authoritative inventory returns Corrected with the held
// count, forces an inventory sync and never creates stash contents.
func TestStashStoreInsufficientInventoryCorrected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-stash-short", "hwid", "StashShort"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-stash-short", "bandage", 1, 1.0)
	sess := ledgerSession(t, s, 7102, 46002, "uuid-stash-short", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	pkt := protocol.StashActionPacket{
		ActionID: 1, Action: protocol.StashActionStore, Count: 5, Condition: 100,
	}
	assembleSection(&pkt.Section, "bandage")
	sendStashAction(t, s, sess, 1, pkt)

	corrected := stashResultsByResult(t, sink)[protocol.ItemResultCorrected]
	if len(corrected) != 1 {
		t.Fatalf("corrected results = %d, want 1", len(corrected))
	}
	if corrected[0].Count != -1 {
		t.Fatalf("correction count = %d, want -1 (server holds 1)", corrected[0].Count)
	}
	if len(packetsByOpcode(sink, protocol.OpInventorySync)) == 0 {
		t.Fatal("correction did not force an inventory sync")
	}
	if items := stashItemsAtKey(t, s, "l01_escape", 0, 0, 0); len(items) != 0 {
		t.Fatalf("stash changed on rejected store: %+v", items)
	}
	if avail, _ := db.AvailableItemCount("uuid-stash-short", "bandage"); avail != 1 {
		t.Fatalf("inventory changed on rejected store: %d, want 1", avail)
	}
	if got := countAuditEvents(t, s, "stash_rejected"); got != 1 {
		t.Errorf("stash_rejected audit rows = %d, want 1", got)
	}
}

// Taking from a key with no stash is rejected, never creates one, and pushes
// the authoritative wallet + inventory back to the client.
func TestStashTakeMissingStashRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-stash-empty", "hwid", "StashEmpty"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	sess := ledgerSession(t, s, 7103, 46003, "uuid-stash-empty", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	pkt := protocol.StashActionPacket{
		ActionID: 1, Action: protocol.StashActionTake, Count: 1, Condition: 100,
	}
	assembleSection(&pkt.Section, "bandage")
	sendStashAction(t, s, sess, 1, pkt)

	if got := stashResultsByResult(t, sink)[protocol.ItemResultRejected]; len(got) != 1 {
		t.Fatalf("missing-stash take results = %d, want 1 rejection", len(got))
	}
	if items := stashItemsAtKey(t, s, "l01_escape", 0, 0, 0); len(items) != 0 {
		t.Fatalf("take created a stash: %+v", items)
	}
	if len(packetsByOpcode(sink, protocol.OpInventorySync)) == 0 {
		t.Fatal("stash rejection did not force an inventory sync")
	}
	if _, ok := lastWalletMoney(t, sink); !ok {
		t.Fatal("stash rejection did not send an OpWalletUpdate")
	}
}

// A replayed 0x0081 ActionID echoes its cached OpStashResult without
// re-applying, and the same ActionID reused on 0x007D is rejected as stale:
// both share the item ledger's monotonic floor.
func TestStashReplaySharesItemLedgerCache(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-stash-shared", "hwid", "StashShared"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-stash-shared", "bandage", 5, 1.0)
	sess := ledgerSession(t, s, 7104, 46004, "uuid-stash-shared", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	store := protocol.StashActionPacket{
		ActionID: 7, Action: protocol.StashActionStore, Count: 2, Condition: 100,
	}
	assembleSection(&store.Section, "bandage")
	sendStashAction(t, s, sess, 1, store)
	if got := stashResultsByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("store OK results = %d, want 1", len(got))
	}

	// Replay under a new sequence: cached echo, no re-apply.
	sink.Reset()
	sendStashAction(t, s, sess, 2, store)
	if got := stashResultsByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("replayed store OK results = %d, want 1", len(got))
	}
	if items := stashItemsAtKey(t, s, "l01_escape", 0, 0, 0); len(items) != 1 || items[0].Count != 2 {
		t.Fatalf("replay changed stash: %+v", items)
	}
	if avail, _ := db.AvailableItemCount("uuid-stash-shared", "bandage"); avail != 3 {
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
	store.ActionID = 8
	sendStashAction(t, s, sess, 4, store)
	if got := stashResultsByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("fresh store OK results = %d, want 1", len(got))
	}
	if items := stashItemsAtKey(t, s, "l01_escape", 0, 0, 0); len(items) != 1 || items[0].Count != 4 {
		t.Fatalf("fresh store stash = %+v, want count 4", items)
	}
}

// Stash actions consume the shared per-second item budget: the sixth action in
// the window is rejected and never applied.
func TestStashRateLimit(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-stash-rate", "hwid", "StashRate"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-stash-rate", "bandage", 10, 1.0)
	sess := ledgerSession(t, s, 7105, 46005, "uuid-stash-rate", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	for i := 0; i < 6; i++ {
		pkt := protocol.StashActionPacket{
			ActionID: uint32(i + 1), Action: protocol.StashActionStore, Count: 1, Condition: 100,
		}
		assembleSection(&pkt.Section, "bandage")
		sendStashAction(t, s, sess, uint32(i+1), pkt)
	}

	results := stashResultsByResult(t, sink)
	if got := len(results[protocol.ItemResultOK]); got != 5 {
		t.Fatalf("accepted stores = %d, want 5 (rate limit item_rate_per_s=5)", got)
	}
	if got := len(results[protocol.ItemResultRejected]); got != 1 {
		t.Fatalf("rate-limited rejections = %d, want 1", got)
	}
	if items := stashItemsAtKey(t, s, "l01_escape", 0, 0, 0); len(items) != 1 || items[0].Count != 5 {
		t.Fatalf("stash after rate limit = %+v, want count 5", items)
	}
}

// Selling removes the exact stack, credits the price once, derives the
// condition from the consumed row and pushes the new wallet balance.
func TestTradeSellCreditsExactlyOnce(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-sell", "hwid", "Seller"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-sell", "bandage", 3, 0.5)
	sess := ledgerSession(t, s, 7201, 47001, "uuid-sell", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	pkt := protocol.TradeActionPacket{
		ActionID:   1,
		Action:     protocol.TradeActionSell,
		MoneyDelta: 150,
		Count:      2,
		Condition:  0,
	}
	assembleSection(&pkt.Section, "bandage")
	sendTradeAction(t, s, sess, 1, pkt)

	ok := tradeResultsByResult(t, sink)[protocol.ItemResultOK]
	if len(ok) != 1 {
		t.Fatalf("sell OK results = %d, want 1", len(ok))
	}
	if ok[0].MoneyDelta != 150 || ok[0].Action != protocol.TradeActionSell {
		t.Fatalf("sell result = %+v, want money=150 action=sell", ok[0])
	}
	if ok[0].Condition != 50 {
		t.Fatalf("sell condition = %d, want 50 derived from the inventory row", ok[0].Condition)
	}
	if got := rublesOf(t, s, "uuid-sell"); got != 5150 {
		t.Fatalf("rubles after sell = %d, want 5150", got)
	}
	if avail, _ := db.AvailableItemCount("uuid-sell", "bandage"); avail != 1 {
		t.Fatalf("inventory after sell = %d, want 1", avail)
	}
	if money, ok := lastWalletMoney(t, sink); !ok || money != 5150 {
		t.Fatalf("wallet after sell = %d (present=%t), want 5150", money, ok)
	}

	// Replay under a new sequence: cached echo, credited exactly once.
	sink.Reset()
	sendTradeAction(t, s, sess, 2, pkt)
	if got := tradeResultsByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("replayed sell OK results = %d, want 1", len(got))
	}
	if got := rublesOf(t, s, "uuid-sell"); got != 5150 {
		t.Fatalf("replay credited again: rubles = %d, want 5150", got)
	}
	if avail, _ := db.AvailableItemCount("uuid-sell", "bandage"); avail != 1 {
		t.Fatalf("replay removed again: %d, want 1", avail)
	}
	if got := countAuditEvents(t, s, "trade_sell"); got != 1 {
		t.Errorf("trade_sell audit rows = %d, want 1", got)
	}
}

// Buying debits the price once, credits the exact count/condition into
// character_inventory and pushes the new wallet balance.
func TestTradeBuyDebitsAndAddsExactlyOnce(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-buy", "hwid", "Buyer"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	sess := ledgerSession(t, s, 7202, 47002, "uuid-buy", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	pkt := protocol.TradeActionPacket{
		ActionID:   1,
		Action:     protocol.TradeActionBuy,
		MoneyDelta: 1200,
		Count:      3,
		Condition:  80,
	}
	assembleSection(&pkt.Section, "medkit")
	sendTradeAction(t, s, sess, 1, pkt)

	ok := tradeResultsByResult(t, sink)[protocol.ItemResultOK]
	if len(ok) != 1 {
		t.Fatalf("buy OK results = %d, want 1", len(ok))
	}
	if ok[0].MoneyDelta != -1200 || ok[0].Action != protocol.TradeActionBuy {
		t.Fatalf("buy result = %+v, want money=-1200 action=buy", ok[0])
	}
	if ok[0].Condition != 80 {
		t.Fatalf("buy condition = %d, want 80", ok[0].Condition)
	}
	if got := rublesOf(t, s, "uuid-buy"); got != 3800 {
		t.Fatalf("rubles after buy = %d, want 3800", got)
	}
	items, err := db.GetCharacterInventory("uuid-buy")
	if err != nil {
		t.Fatalf("GetCharacterInventory: %v", err)
	}
	if len(items) != 1 || items[0].ItemSection != "medkit" || items[0].ItemCount != 3 {
		t.Fatalf("inventory after buy = %+v, want one medkit stack of 3", items)
	}
	if got := int(items[0].Condition*100 + 0.5); got != 80 {
		t.Fatalf("medkit condition = %d, want 80", got)
	}
	if money, ok := lastWalletMoney(t, sink); !ok || money != 3800 {
		t.Fatalf("wallet after buy = %d (present=%t), want 3800", money, ok)
	}

	// Replay under a new sequence: cached echo, debited exactly once.
	sink.Reset()
	sendTradeAction(t, s, sess, 2, pkt)
	if got := tradeResultsByResult(t, sink)[protocol.ItemResultOK]; len(got) != 1 {
		t.Fatalf("replayed buy OK results = %d, want 1", len(got))
	}
	if got := rublesOf(t, s, "uuid-buy"); got != 3800 {
		t.Fatalf("replay debited again: rubles = %d, want 3800", got)
	}
	items, _ = db.GetCharacterInventory("uuid-buy")
	if len(items) != 1 || items[0].ItemCount != 3 {
		t.Fatalf("replay added items again: %+v", items)
	}
	if got := countAuditEvents(t, s, "trade_buy"); got != 1 {
		t.Errorf("trade_buy audit rows = %d, want 1", got)
	}
}

// A buy above the authoritative balance is rejected, changes nothing and still
// pushes the authoritative wallet.
func TestTradeInsufficientMoneyRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-broke", "hwid", "Broke"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	sess := ledgerSession(t, s, 7203, 47003, "uuid-broke", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	pkt := protocol.TradeActionPacket{
		ActionID: 1, Action: protocol.TradeActionBuy, MoneyDelta: 9999, Count: 1, Condition: 100,
	}
	assembleSection(&pkt.Section, "medkit")
	sendTradeAction(t, s, sess, 1, pkt)

	rejected := tradeResultsByResult(t, sink)[protocol.ItemResultRejected]
	if len(rejected) != 1 {
		t.Fatalf("insufficient-funds buy results = %d, want 1 rejection", len(rejected))
	}
	if rejected[0].MoneyDelta != 0 {
		t.Fatalf("rejected buy money delta = %d, want 0", rejected[0].MoneyDelta)
	}
	if got := rublesOf(t, s, "uuid-broke"); got != 5000 {
		t.Fatalf("rubles changed on rejected buy: %d, want 5000", got)
	}
	if avail, _ := db.AvailableItemCount("uuid-broke", "medkit"); avail != 0 {
		t.Fatalf("rejected buy added items: %d", avail)
	}
	if money, ok := lastWalletMoney(t, sink); !ok || money != 5000 {
		t.Fatalf("wallet on rejected buy = %d (present=%t), want 5000", money, ok)
	}
	if got := countAuditEvents(t, s, "trade_rejected"); got != 1 {
		t.Errorf("trade_rejected audit rows = %d, want 1", got)
	}
}

// A sell above the configured cap is clamped to trade_max_money_delta and the
// caller is told with result=Corrected.
func TestTradeMoneyDeltaCapped(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-cap-trade", "hwid", "CapTrade"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	mustExec(t, s, "INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
		"uuid-cap-trade", "artifact", 1, 1.0)
	sess := ledgerSession(t, s, 7204, 47004, "uuid-cap-trade", "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()

	pkt := protocol.TradeActionPacket{
		ActionID: 1, Action: protocol.TradeActionSell, MoneyDelta: 500000, Count: 1, Condition: 100,
	}
	assembleSection(&pkt.Section, "artifact")
	sendTradeAction(t, s, sess, 1, pkt)

	corrected := tradeResultsByResult(t, sink)[protocol.ItemResultCorrected]
	if len(corrected) != 1 {
		t.Fatalf("capped sell results = %d, want 1 corrected", len(corrected))
	}
	if corrected[0].MoneyDelta != 200000 {
		t.Fatalf("capped money delta = %d, want 200000", corrected[0].MoneyDelta)
	}
	if got := rublesOf(t, s, "uuid-cap-trade"); got != 205000 {
		t.Fatalf("rubles after capped sell = %d, want 205000", got)
	}
}

// A returning player receives OpWalletUpdate with the persisted balance as
// part of the join flow.
func TestWalletUpdateSentOnJoin(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-join-wallet", "hwid-join", "JoinWallet"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-join-wallet", "stalker", "bandage", 1234); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 48001}
	doJoinHandshake(t, s, addr, "uuid-join-wallet", "JoinWallet")

	money, ok := lastWalletMoney(t, sink)
	if !ok {
		t.Fatal("join did not send OpWalletUpdate")
	}
	if money != 1234 {
		t.Fatalf("wallet on join = %d, want 1234", money)
	}
}
