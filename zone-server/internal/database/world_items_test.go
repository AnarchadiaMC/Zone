package database

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openLedgerTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	db.RawDB().SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := db.AutoProvision("uuid-ledger", "hwid-ledger", "Ledger"); err != nil {
		t.Fatalf("AutoProvision failed: %v", err)
	}
	return db
}

func seedInventory(t *testing.T, db *DB, uuid, section string, count int) {
	t.Helper()
	if _, err := db.RawDB().Exec(
		"INSERT INTO character_inventory (client_uuid, item_section, item_count) VALUES (?, ?, ?)",
		uuid, section, count); err != nil {
		t.Fatalf("seed inventory failed: %v", err)
	}
}

func TestWorldItemsSchemaAndMigration(t *testing.T) {
	// Fresh database: world_items exists with the ledger columns.
	db := openLedgerTestDB(t)
	if _, err := db.RawDB().Exec(
		"INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section, item_count, condition) VALUES ('l01_escape', 1, 2, 3, 'bandage', 4, 0.5)"); err != nil {
		t.Fatalf("fresh world_items insert failed: %v", err)
	}

	// Legacy database: character_inventory without item_count gets the column
	// added by the Open-time migration.
	path := filepath.Join(t.TempDir(), "legacy_inventory.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw sql.Open failed: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE character_inventory (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		client_uuid TEXT NOT NULL,
		item_section TEXT NOT NULL,
		condition REAL NOT NULL DEFAULT 1.0,
		ammo_current INTEGER NOT NULL DEFAULT 0,
		addon_flags INTEGER NOT NULL DEFAULT 0,
		slot INTEGER NOT NULL DEFAULT -1,
		grid_x INTEGER NOT NULL DEFAULT 0,
		grid_y INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		raw.Close()
		t.Fatalf("legacy schema create failed: %v", err)
	}
	raw.Close()

	legacy, err := Open(path)
	if err != nil {
		t.Fatalf("Open(legacy) failed: %v", err)
	}
	defer legacy.Close()
	rows, err := legacy.RawDB().Query("PRAGMA table_info(character_inventory)")
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	defer rows.Close()
	hasCount := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "item_count" {
			hasCount = true
		}
	}
	if !hasCount {
		t.Fatal("legacy character_inventory did not gain item_count")
	}
}

func TestDropItemToWorldDecrementsAndInserts(t *testing.T) {
	db := openLedgerTestDB(t)
	seedInventory(t, db, "uuid-ledger", "bandage", 5)
	// Server-authoritative condition: the drop derives it from the inventory
	// row, so the client-visible preferred bucket (100) is ignored here.
	if _, err := db.RawDB().Exec("UPDATE character_inventory SET condition=0.75 WHERE client_uuid='uuid-ledger'"); err != nil {
		t.Fatalf("set condition failed: %v", err)
	}

	newID, remaining, condition, err := db.DropItemToWorld("uuid-ledger", "l01_escape", "bandage", 2, 10, -20, 30, 100, 0)
	if err != nil {
		t.Fatalf("DropItemToWorld failed: %v", err)
	}
	if newID <= 0 {
		t.Fatalf("newID = %d, want > 0", newID)
	}
	if remaining != 3 {
		t.Fatalf("remaining = %d, want 3", remaining)
	}
	if condition != 0.75 {
		t.Fatalf("derived condition = %v, want 0.75 from the inventory row", condition)
	}

	items, err := db.GetCharacterInventory("uuid-ledger")
	if err != nil {
		t.Fatalf("GetCharacterInventory failed: %v", err)
	}
	total := 0
	for _, it := range items {
		if it.ItemSection == "bandage" {
			total += it.ItemCount
		}
	}
	if total != 3 {
		t.Fatalf("inventory bandage count = %d, want 3", total)
	}

	wi, err := db.GetWorldItem(newID)
	if err != nil {
		t.Fatalf("GetWorldItem failed: %v", err)
	}
	if wi.Section != "bandage" || wi.Count != 2 || wi.LevelName != "l01_escape" {
		t.Fatalf("world item = %+v, want bandage x2 on l01_escape", wi)
	}
	if wi.Condition != 0.75 {
		t.Fatalf("condition = %v, want 0.75", wi.Condition)
	}
}

func TestDropItemToWorldInsufficientLeavesInventory(t *testing.T) {
	db := openLedgerTestDB(t)
	seedInventory(t, db, "uuid-ledger", "medkit", 2)

	newID, remaining, _, err := db.DropItemToWorld("uuid-ledger", "l01_escape", "medkit", 5, 0, 0, 0, -1, 0)
	if !errors.Is(err, ErrInsufficientItems) {
		t.Fatalf("err = %v, want ErrInsufficientItems", err)
	}
	if newID != 0 || remaining != 2 {
		t.Fatalf("newID=%d remaining=%d, want 0/2", newID, remaining)
	}
	avail, err := db.AvailableItemCount("uuid-ledger", "medkit")
	if err != nil || avail != 2 {
		t.Fatalf("available = %d err=%v, want 2", avail, err)
	}
	var worldRows int
	if err := db.RawDB().QueryRow("SELECT COUNT(*) FROM world_items").Scan(&worldRows); err != nil {
		t.Fatalf("world_items count failed: %v", err)
	}
	if worldRows != 0 {
		t.Fatalf("world_items rows = %d, want 0 after failed drop", worldRows)
	}
}

func TestPickupWorldItemDeletesAndCreditsOnce(t *testing.T) {
	db := openLedgerTestDB(t)
	seedInventory(t, db, "uuid-ledger", "bandage", 1)

	newID, _, _, err := db.DropItemToWorld("uuid-ledger", "l01_escape", "bandage", 1, 5, 5, 5, -1, 0)
	if err != nil {
		t.Fatalf("drop failed: %v", err)
	}
	if avail, _ := db.AvailableItemCount("uuid-ledger", "bandage"); avail != 0 {
		t.Fatalf("pre-pickup available = %d, want 0", avail)
	}

	picked, err := db.PickupWorldItem("uuid-ledger", newID)
	if err != nil {
		t.Fatalf("PickupWorldItem failed: %v", err)
	}
	if picked.Section != "bandage" || picked.Count != 1 {
		t.Fatalf("picked = %+v, want bandage x1 from DB", picked)
	}
	if _, err := db.GetWorldItem(newID); !errors.Is(err, ErrWorldItemNotFound) {
		t.Fatalf("GetWorldItem after pickup = %v, want ErrWorldItemNotFound", err)
	}
	if avail, _ := db.AvailableItemCount("uuid-ledger", "bandage"); avail != 1 {
		t.Fatalf("post-pickup available = %d, want 1", avail)
	}

	// Second pickup of the same id must not duplicate.
	if _, err := db.PickupWorldItem("uuid-ledger", newID); !errors.Is(err, ErrWorldItemNotFound) {
		t.Fatalf("second pickup = %v, want ErrWorldItemNotFound", err)
	}
	if avail, _ := db.AvailableItemCount("uuid-ledger", "bandage"); avail != 1 {
		t.Fatalf("available after double pickup = %d, want 1", avail)
	}
}

func TestPickupWorldItemConcurrentSingleWinner(t *testing.T) {
	db := openLedgerTestDB(t)
	seedInventory(t, db, "uuid-ledger", "bandage", 1)
	newID, _, _, err := db.DropItemToWorld("uuid-ledger", "l01_escape", "bandage", 1, 0, 0, 0, -1, 0)
	if err != nil {
		t.Fatalf("drop failed: %v", err)
	}

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := db.PickupWorldItem("uuid-ledger", newID); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("successful pickups = %d, want exactly 1", success)
	}
	if avail, _ := db.AvailableItemCount("uuid-ledger", "bandage"); avail != 1 {
		t.Fatalf("credited count = %d, want 1", avail)
	}
}

func TestInsertAudit(t *testing.T) {
	db := openLedgerTestDB(t)
	if err := db.InsertAudit("uuid-ledger", "damage_rejected", "forged packet rejected"); err != nil {
		t.Fatalf("InsertAudit failed: %v", err)
	}
	var detail string
	if err := db.RawDB().QueryRow("SELECT detail FROM audit_log WHERE event_type='damage_rejected'").Scan(&detail); err != nil {
		t.Fatalf("audit query failed: %v", err)
	}
	if detail != "forged packet rejected" {
		t.Fatalf("detail = %q, want 'forged packet rejected'", detail)
	}
}

// A stack of 30 must survive a 1-copy removal as 29, not disappear.
func TestRemoveInventoryItemsPreservesStack(t *testing.T) {
	db := openLedgerTestDB(t)
	seedInventory(t, db, "uuid-ledger", "bandage", 30)

	tx, err := db.RawDB().Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()
	total, condition, err := RemoveInventoryItemsLocked(tx, "uuid-ledger", "bandage", 1, -1)
	if err != nil || total != 30 || condition != 1.0 {
		t.Fatalf("RemoveInventoryItemsLocked = total %d cond %v err %v, want 30/1.0/nil", total, condition, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	items, err := db.GetCharacterInventory("uuid-ledger")
	if err != nil {
		t.Fatalf("GetCharacterInventory: %v", err)
	}
	if len(items) != 1 || items[0].ItemCount != 29 {
		t.Fatalf("inventory after 1-of-30 removal = %+v, want one row with count 29", items)
	}
}

func TestDropItemToWorldPerLevelCap(t *testing.T) {
	db := openLedgerTestDB(t)
	seedInventory(t, db, "uuid-ledger", "bandage", 5)
	if _, err := db.RawDB().Exec(
		"INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section) VALUES ('l01_escape', 0, 0, 0, 'medkit')"); err != nil {
		t.Fatalf("seed world item failed: %v", err)
	}

	_, _, _, err := db.DropItemToWorld("uuid-ledger", "l01_escape", "bandage", 1, 1, 1, 1, -1, 1)
	if !errors.Is(err, ErrWorldItemLimit) {
		t.Fatalf("capped drop err = %v, want ErrWorldItemLimit", err)
	}
	if avail, _ := db.AvailableItemCount("uuid-ledger", "bandage"); avail != 5 {
		t.Fatalf("capped drop mutated inventory: %d, want 5", avail)
	}

	// The cap is per level; another level still accepts drops.
	if _, _, _, err := db.DropItemToWorld("uuid-ledger", "l02_garbage", "bandage", 1, 1, 1, 1, -1, 1); err != nil {
		t.Fatalf("drop on uncapped level failed: %v", err)
	}
}

func TestDeleteWorldItemsOlderThan(t *testing.T) {
	db := openLedgerTestDB(t)
	if _, err := db.RawDB().Exec(
		`INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section, created_at) VALUES ('l01_escape', 0, 0, 0, 'old_item', '2000-01-01 00:00:00')`); err != nil {
		t.Fatalf("seed old item failed: %v", err)
	}
	if _, err := db.RawDB().Exec(
		`INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section) VALUES ('l01_escape', 0, 0, 0, 'fresh_item')`); err != nil {
		t.Fatalf("seed fresh item failed: %v", err)
	}

	expired, err := db.DeleteWorldItemsOlderThan(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("DeleteWorldItemsOlderThan: %v", err)
	}
	if len(expired) != 1 || expired[0].Section != "old_item" {
		t.Fatalf("expired = %+v, want the old_item row only", expired)
	}
	var remaining int
	if err := db.RawDB().QueryRow("SELECT COUNT(*) FROM world_items").Scan(&remaining); err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining world items = %d, want 1", remaining)
	}
}

// Credits merge by section + condition bucket: a 50% credit never repairs a
// 100% stack.
func TestCreditInventoryMergesByConditionBucket(t *testing.T) {
	db := openLedgerTestDB(t)
	if _, err := db.RawDB().Exec(
		"INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES ('uuid-ledger', 'bandage', 1, 0.5)"); err != nil {
		t.Fatalf("seed 0.5 row failed: %v", err)
	}
	if _, err := db.RawDB().Exec(
		"INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES ('uuid-ledger', 'bandage', 1, 1.0)"); err != nil {
		t.Fatalf("seed 1.0 row failed: %v", err)
	}

	tx, err := db.RawDB().Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := CreditInventoryLocked(tx, "uuid-ledger", "bandage", 2, 50); err != nil {
		tx.Rollback()
		t.Fatalf("CreditInventoryLocked: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	byCondition := map[float32]int{}
	items, err := db.GetCharacterInventory("uuid-ledger")
	if err != nil {
		t.Fatalf("GetCharacterInventory: %v", err)
	}
	for _, it := range items {
		byCondition[float32(int(it.Condition*100+0.5))] += it.ItemCount
	}
	if len(items) != 2 || byCondition[50] != 3 || byCondition[100] != 1 {
		t.Fatalf("credit buckets = %+v (rows %+v), want 50:3 100:1", byCondition, items)
	}
}

// A single-object row with loaded rounds receives credited counts as
// ammo_current instead of materialising zero-ammo copies (documented ammo
// limitation of the wire protocol).
func TestCreditInventoryAmmoSemantics(t *testing.T) {
	db := openLedgerTestDB(t)
	if _, err := db.RawDB().Exec(
		"INSERT INTO character_inventory (client_uuid, item_section, item_count, condition, ammo_current) VALUES ('uuid-ledger', 'wpn_pm', 1, 1.0, 30)"); err != nil {
		t.Fatalf("seed ammo row failed: %v", err)
	}

	tx, err := db.RawDB().Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := CreditInventoryLocked(tx, "uuid-ledger", "wpn_pm", 15, 100); err != nil {
		tx.Rollback()
		t.Fatalf("CreditInventoryLocked: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	items, err := db.GetCharacterInventory("uuid-ledger")
	if err != nil {
		t.Fatalf("GetCharacterInventory: %v", err)
	}
	if len(items) != 1 || items[0].ItemCount != 1 || items[0].AmmoCurrent != 45 {
		t.Fatalf("ammo credit = %+v, want one object with ammo 45", items)
	}
}
