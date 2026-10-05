package game

import (
	"errors"
	"testing"

	"zone-online/zone-server/internal/database"
)

func setupTestDB(t *testing.T) (*database.DB, string) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("Failed to open test in-memory db: %v", err)
	}

	testUUID := "test-uuid-stalker-1"
	if err := db.AutoProvision(testUUID, "hwid-hash-1", "TestStalker"); err != nil {
		t.Fatalf("Failed to auto-provision test character: %v", err)
	}
	return db, testUUID
}

func TestEconomyManager(t *testing.T) {
	t.Run("DB Not Configured", func(t *testing.T) {
		mgr := NewEconomyManager()
		_, err := mgr.GetBalance("some-uuid")
		if !errors.Is(err, ErrDBNotConfigured) {
			t.Errorf("Expected ErrDBNotConfigured, got %v", err)
		}
	})

	t.Run("GetBalance and Character Not Found", func(t *testing.T) {
		db, testUUID := setupTestDB(t)
		mgr := NewEconomyManager(db)

		bal, err := mgr.GetBalance(testUUID)
		if err != nil {
			t.Fatalf("Unexpected error getting balance: %v", err)
		}
		if bal != 5000 {
			t.Errorf("Expected initial balance 5000, got %d", bal)
		}

		_, err = mgr.GetBalance("non-existent-uuid")
		if !errors.Is(err, ErrCharacterNotFound) {
			t.Errorf("Expected ErrCharacterNotFound, got %v", err)
		}
	})

	t.Run("AddCurrency", func(t *testing.T) {
		db, testUUID := setupTestDB(t)
		mgr := NewEconomyManager(db)

		// Add positive amount
		if err := mgr.AddCurrency(testUUID, 1500); err != nil {
			t.Fatalf("Failed to add currency: %v", err)
		}
		bal, err := mgr.GetBalance(testUUID)
		if err != nil || bal != 6500 {
			t.Errorf("Expected 6500, got %d (err: %v)", bal, err)
		}

		// Deduct valid amount
		if err := mgr.AddCurrency(testUUID, -2500); err != nil {
			t.Fatalf("Failed to deduct currency: %v", err)
		}
		bal, err = mgr.GetBalance(testUUID)
		if err != nil || bal != 4000 {
			t.Errorf("Expected 4000, got %d (err: %v)", bal, err)
		}

		// Deduct too much (insufficient funds)
		if err := mgr.AddCurrency(testUUID, -10000); !errors.Is(err, ErrInsufficientFunds) {
			t.Errorf("Expected ErrInsufficientFunds, got %v", err)
		}
		bal, _ = mgr.GetBalance(testUUID)
		if bal != 4000 {
			t.Errorf("Balance changed after failed deduction: got %d, expected 4000", bal)
		}
	})

	t.Run("BuyItem and SellItem", func(t *testing.T) {
		db, testUUID := setupTestDB(t)
		mgr := NewEconomyManager(db)

		// Initial balance is 5000
		// Buy an item within budget
		ok, err := mgr.BuyItem(testUUID, "wpn_ak74", 3000)
		if err != nil {
			t.Fatalf("BuyItem failed: %v", err)
		}
		if !ok {
			t.Fatalf("BuyItem returned false, expected true")
		}

		bal, _ := mgr.GetBalance(testUUID)
		if bal != 2000 {
			t.Errorf("Expected balance 2000 after buy, got %d", bal)
		}

		// Verify item exists in inventory
		inv, err := db.GetCharacterInventory(testUUID)
		if err != nil {
			t.Fatalf("Failed to get inventory: %v", err)
		}
		if len(inv) != 1 || inv[0].ItemSection != "wpn_ak74" {
			t.Errorf("Unexpected inventory after buy: %+v", inv)
		}

		// Try to buy an item beyond remaining budget (2000 available, costs 2500)
		ok, err = mgr.BuyItem(testUUID, "wpn_vintorez", 2500)
		if err != nil {
			t.Fatalf("BuyItem returned error: %v", err)
		}
		if ok {
			t.Errorf("Expected BuyItem to return false due to insufficient funds")
		}
		bal, _ = mgr.GetBalance(testUUID)
		if bal != 2000 {
			t.Errorf("Balance changed after failed buy: got %d, expected 2000", bal)
		}

		// Sell the item we bought
		ok, err = mgr.SellItem(testUUID, "wpn_ak74", 1500)
		if err != nil {
			t.Fatalf("SellItem failed: %v", err)
		}
		if !ok {
			t.Fatalf("SellItem returned false, expected true")
		}
		bal, _ = mgr.GetBalance(testUUID)
		if bal != 3500 {
			t.Errorf("Expected balance 3500 after sell, got %d", bal)
		}

		// Verify item is removed from inventory
		inv, _ = db.GetCharacterInventory(testUUID)
		if len(inv) != 0 {
			t.Errorf("Expected empty inventory after sell, got %+v", inv)
		}

		// Try selling again when we don't have it
		ok, err = mgr.SellItem(testUUID, "wpn_ak74", 1500)
		if err != nil {
			t.Fatalf("SellItem returned error: %v", err)
		}
		if ok {
			t.Errorf("Expected SellItem to return false when item is not in inventory")
		}
	})

	t.Run("SellItemRemovesOneFromStack", func(t *testing.T) {
		db, testUUID := setupTestDB(t)
		mgr := NewEconomyManager(db)

		if _, err := db.RawDB().Exec(
			"INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, 'bandage', 3, 1.0)",
			testUUID); err != nil {
			t.Fatalf("seed stack: %v", err)
		}

		// One sale pays one price and leaves the rest of the stack intact.
		ok, err := mgr.SellItem(testUUID, "bandage", 150)
		if err != nil || !ok {
			t.Fatalf("SellItem = %v/%v, want true/nil", ok, err)
		}
		bal, _ := mgr.GetBalance(testUUID)
		if bal != 5150 {
			t.Fatalf("balance after one sale = %d, want 5150 (one unit's price)", bal)
		}
		inv, err := db.GetCharacterInventory(testUUID)
		if err != nil {
			t.Fatalf("GetCharacterInventory: %v", err)
		}
		if len(inv) != 1 || inv[0].ItemCount != 2 {
			t.Fatalf("inventory after one sale = %+v, want one row with count 2", inv)
		}

		// Draining the remaining stack deletes the row and pays each unit.
		for i := 0; i < 2; i++ {
			if ok, err := mgr.SellItem(testUUID, "bandage", 150); err != nil || !ok {
				t.Fatalf("SellItem drain %d = %v/%v, want true/nil", i, ok, err)
			}
		}
		inv, _ = db.GetCharacterInventory(testUUID)
		if len(inv) != 0 {
			t.Fatalf("inventory after drain = %+v, want empty", inv)
		}
		bal, _ = mgr.GetBalance(testUUID)
		if bal != 5450 {
			t.Fatalf("balance after drain = %d, want 5450", bal)
		}
	})

	t.Run("Tier Progression", func(t *testing.T) {
		db, testUUID := setupTestDB(t)
		mgr := NewEconomyManager(db)

		tier, err := mgr.GetTier(testUUID)
		if err != nil {
			t.Fatalf("GetTier failed: %v", err)
		}
		if tier != 1 {
			t.Errorf("Expected default tier 1, got %d", tier)
		}

		if err := mgr.SetTier(testUUID, 3); err != nil {
			t.Fatalf("SetTier failed: %v", err)
		}

		tier, err = mgr.GetTier(testUUID)
		if err != nil || tier != 3 {
			t.Errorf("Expected tier 3 after set, got %d (err: %v)", tier, err)
		}
	})
}
