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
