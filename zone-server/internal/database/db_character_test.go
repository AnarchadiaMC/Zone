package database

import "testing"

func TestCreateCharacter_AppliesMoneyAndInventory(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}
	defer db.Close()

	if err := db.AutoProvision("uuid-money", "hwid", "MoneyMan"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-money", "bandit", "medkit:2,bandage:3", 2500); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}

	char, err := db.LoadCharacter("uuid-money")
	if err != nil {
		t.Fatalf("LoadCharacter: %v", err)
	}
	if char.Faction != "bandit" {
		t.Errorf("expected faction bandit, got %q", char.Faction)
	}
	if char.ProfileRev != 1 {
		t.Errorf("expected profile_rev 1, got %d", char.ProfileRev)
	}
	if char.Health != 100.0 {
		t.Errorf("expected full 100-scale health for a new character, got %v", char.Health)
	}

	var rubles int
	if err := db.RawDB().QueryRow("SELECT rubles FROM characters WHERE client_uuid=?", "uuid-money").Scan(&rubles); err != nil {
		t.Fatalf("read rubles: %v", err)
	}
	if rubles != 2500 {
		t.Errorf("expected rubles 2500, got %d", rubles)
	}

	items, err := db.GetCharacterInventory("uuid-money")
	if err != nil {
		t.Fatalf("GetCharacterInventory: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 inventory rows, got %d", len(items))
	}
}

// The client loadout grammar is section:stackcount (the value
// zone_ui_peer_faction.script BuildLoadoutItems emits); the count must land in
// item_count, matching what the drop/pickup ledger enforces.
func TestCreateCharacter_LoadoutStoresStackCount(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}
	defer db.Close()

	if err := db.AutoProvision("uuid-stack", "hwid", "Stacker"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-stack", "stalker", "ammo_5.45x39_fmj:60,medkit:2,wpn_pm:1", 0); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}

	items, err := db.GetCharacterInventory("uuid-stack")
	if err != nil {
		t.Fatalf("GetCharacterInventory: %v", err)
	}
	bySection := map[string]InventoryItem{}
	for _, it := range items {
		bySection[it.ItemSection] = it
	}
	want := map[string]int{"ammo_5.45x39_fmj": 60, "medkit": 2, "wpn_pm": 1}
	for sec, count := range want {
		got, ok := bySection[sec]
		if !ok {
			t.Fatalf("section %s missing from inventory: %+v", sec, items)
		}
		if got.ItemCount != count {
			t.Errorf("section %s item_count = %d, want %d", sec, got.ItemCount, count)
		}
		if got.AmmoCurrent != 0 {
			t.Errorf("section %s ammo_current = %d, want 0 (count is a stack count)", sec, got.AmmoCurrent)
		}
	}
}

func TestCreateCharacter_ZeroMoneyKeepsDefault(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}
	defer db.Close()

	if err := db.AutoProvision("uuid-zero", "hwid", "ZeroMan"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-zero", "stalker", "", 0); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}

	var rubles int
	if err := db.RawDB().QueryRow("SELECT rubles FROM characters WHERE client_uuid=?", "uuid-zero").Scan(&rubles); err != nil {
		t.Fatalf("read rubles: %v", err)
	}
	if rubles != 5000 {
		t.Errorf("expected schema default 5000 rubles, got %d", rubles)
	}
}
