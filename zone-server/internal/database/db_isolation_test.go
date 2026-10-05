package database

import "testing"

// Two-account regression tests. Every persistence query keys on client_uuid;
// these tests mutate account A and assert account B's inventory, money and
// progression row are byte-for-byte untouched.

func openIsolationTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	db.RawDB().SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func provisionIsolationAccount(t *testing.T, db *DB, uuid, nick, faction, loadout string, money uint32) {
	t.Helper()
	if err := db.AutoProvision(uuid, "hwid-"+uuid, nick); err != nil {
		t.Fatalf("AutoProvision(%s): %v", uuid, err)
	}
	if err := db.CreateCharacter(uuid, faction, loadout, money); err != nil {
		t.Fatalf("CreateCharacter(%s): %v", uuid, err)
	}
}

func mustExecIsolation(t *testing.T, db *DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.RawDB().Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func inventoryBySection(t *testing.T, db *DB, uuid string) map[string]InventoryItem {
	t.Helper()
	items, err := db.GetCharacterInventory(uuid)
	if err != nil {
		t.Fatalf("GetCharacterInventory(%s): %v", uuid, err)
	}
	out := make(map[string]InventoryItem, len(items))
	for _, it := range items {
		out[it.ItemSection] = it
	}
	return out
}

func availableIsolation(t *testing.T, db *DB, uuid, section string) int {
	t.Helper()
	n, err := db.AvailableItemCount(uuid, section)
	if err != nil {
		t.Fatalf("AvailableItemCount(%s, %s): %v", uuid, section, err)
	}
	return n
}

func rublesIsolation(t *testing.T, db *DB, uuid string) int {
	t.Helper()
	var rubles int
	if err := db.RawDB().QueryRow("SELECT rubles FROM characters WHERE client_uuid=?", uuid).Scan(&rubles); err != nil {
		t.Fatalf("read rubles(%s): %v", uuid, err)
	}
	return rubles
}

func TestInventoryIsolationBetweenAccounts(t *testing.T) {
	db := openIsolationTestDB(t)
	const alice, bob = "uuid-iso-alice", "uuid-iso-bob"
	provisionIsolationAccount(t, db, alice, "Alice", "stalker", "", 1000)
	provisionIsolationAccount(t, db, bob, "Bob", "bandit", "", 9999)

	// Distinct sections, counts, conditions and money per account.
	mustExecIsolation(t, db, `INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, 'bandage', 5, 0.25)`, alice)
	mustExecIsolation(t, db, `INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, 'medkit', 2, 1.0)`, alice)
	mustExecIsolation(t, db, `INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, 'bandage', 9, 0.75)`, bob)
	mustExecIsolation(t, db, `INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, 'ammo_5.45x39_fmj', 30, 1.0)`, bob)

	invA := inventoryBySection(t, db, alice)
	invB := inventoryBySection(t, db, bob)

	if got := invA["bandage"]; got.ItemCount != 5 || got.Condition != 0.25 {
		t.Fatalf("Alice bandage = %+v, want count 5 condition 0.25", got)
	}
	if got := invB["bandage"]; got.ItemCount != 9 || got.Condition != 0.75 {
		t.Fatalf("Bob bandage = %+v, want count 9 condition 0.75", got)
	}
	if _, ok := invA["ammo_5.45x39_fmj"]; ok {
		t.Fatal("Alice's inventory read leaked Bob's ammo row")
	}
	if _, ok := invB["medkit"]; ok {
		t.Fatal("Bob's inventory read leaked Alice's medkit row")
	}

	// Consuming from Alice changes only Alice.
	if remaining, err := db.ConsumeItem(alice, "bandage", 2); err != nil || remaining != 3 {
		t.Fatalf("ConsumeItem(alice) = %d, %v; want 3, nil", remaining, err)
	}
	if got := availableIsolation(t, db, bob, "bandage"); got != 9 {
		t.Fatalf("Bob bandage after Alice consume = %d, want 9", got)
	}

	// Dropping from Alice to the world changes only Alice (+ one world row).
	if _, _, _, err := db.DropItemToWorld(alice, "l01_escape", "bandage", 1, 0, 0, 0, -1, 0); err != nil {
		t.Fatalf("DropItemToWorld(alice): %v", err)
	}
	if got := availableIsolation(t, db, alice, "bandage"); got != 2 {
		t.Fatalf("Alice bandage after drop = %d, want 2", got)
	}
	if got := availableIsolation(t, db, bob, "bandage"); got != 9 {
		t.Fatalf("Bob bandage after Alice drop = %d, want 9", got)
	}
	if got := availableIsolation(t, db, bob, "ammo_5.45x39_fmj"); got != 30 {
		t.Fatalf("Bob ammo after Alice drop = %d, want 30", got)
	}

	if got := rublesIsolation(t, db, alice); got != 1000 {
		t.Fatalf("Alice rubles = %d, want 1000", got)
	}
	if got := rublesIsolation(t, db, bob); got != 9999 {
		t.Fatalf("Bob rubles = %d, want 9999", got)
	}
}

func TestProgressionIsolationBetweenAccounts(t *testing.T) {
	db := openIsolationTestDB(t)
	const alice, bob = "uuid-prog-alice", "uuid-prog-bob"
	provisionIsolationAccount(t, db, alice, "Alice", "stalker", "", 1000)
	provisionIsolationAccount(t, db, bob, "Bob", "bandit", "", 4321)

	base, err := db.LoadCharacter(bob)
	if err != nil {
		t.Fatalf("LoadCharacter(bob): %v", err)
	}
	baseRubles := rublesIsolation(t, db, bob)

	// Mutate Alice's progression row and transform.
	aliceChar, err := db.LoadCharacter(alice)
	if err != nil {
		t.Fatalf("LoadCharacter(alice): %v", err)
	}
	aliceChar.LevelName = "l02_garbage"
	aliceChar.PosX, aliceChar.PosY, aliceChar.PosZ = 11, 12, 13
	aliceChar.Yaw = 1.5
	aliceChar.Faction = "freedom"
	aliceChar.Health = 0.25
	if err := db.SaveCharacter(aliceChar); err != nil {
		t.Fatalf("SaveCharacter(alice): %v", err)
	}
	if err := db.FlushPlayerTransform(alice, 21, 22, 23, 0.5, 40); err != nil {
		t.Fatalf("FlushPlayerTransform(alice): %v", err)
	}
	mustExecIsolation(t, db, `UPDATE characters SET economy_tier=3, reputation=42 WHERE client_uuid=?`, alice)

	// Bob's row is unchanged.
	after, err := db.LoadCharacter(bob)
	if err != nil {
		t.Fatalf("LoadCharacter(bob) after: %v", err)
	}
	if after.LevelName != base.LevelName || after.PosX != base.PosX || after.PosY != base.PosY ||
		after.PosZ != base.PosZ || after.Yaw != base.Yaw || after.Faction != base.Faction ||
		after.Health != base.Health {
		t.Fatalf("Bob progression changed after Alice mutation:\n before=%+v\n after =%+v", base, after)
	}
	if got := rublesIsolation(t, db, bob); got != baseRubles {
		t.Fatalf("Bob rubles changed from %d to %d", baseRubles, got)
	}
	var bobTier, bobRep int
	if err := db.RawDB().QueryRow("SELECT economy_tier, reputation FROM characters WHERE client_uuid=?", bob).Scan(&bobTier, &bobRep); err != nil {
		t.Fatalf("read Bob tier/reputation: %v", err)
	}
	if bobTier != 1 || bobRep != 0 {
		t.Fatalf("Bob tier/reputation = %d/%d, want 1/0", bobTier, bobRep)
	}

	// Alice's mutation did land on Alice's row.
	mutated, err := db.LoadCharacter(alice)
	if err != nil {
		t.Fatalf("LoadCharacter(alice) after: %v", err)
	}
	if mutated.LevelName != "l02_garbage" || mutated.PosX != 21 || mutated.Faction != "freedom" || mutated.Health != 40 {
		t.Fatalf("Alice mutation not persisted: %+v", mutated)
	}
	var aliceTier, aliceRep int
	if err := db.RawDB().QueryRow("SELECT economy_tier, reputation FROM characters WHERE client_uuid=?", alice).Scan(&aliceTier, &aliceRep); err != nil {
		t.Fatalf("read Alice tier/reputation: %v", err)
	}
	if aliceTier != 3 || aliceRep != 42 {
		t.Fatalf("Alice tier/reputation = %d/%d, want 3/42", aliceTier, aliceRep)
	}
}
