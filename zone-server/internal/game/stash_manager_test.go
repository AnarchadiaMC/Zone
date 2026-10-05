package game

import (
	"encoding/json"
	"errors"
	"testing"

	"zone-online/zone-server/internal/database"
)

func TestStashManager(t *testing.T) {
	t.Run("DB Not Configured", func(t *testing.T) {
		mgr := NewStashManager()
		_, err := mgr.GetStash(1)
		if !errors.Is(err, ErrDBNotConfigured) {
			t.Errorf("Expected ErrDBNotConfigured, got %v", err)
		}
	})

	t.Run("Save and Get Stash", func(t *testing.T) {
		db, err := database.Open(":memory:")
		if err != nil {
			t.Fatalf("Failed to open test db: %v", err)
		}
		mgr := NewStashManager(db)

		stashID := uint32(101)
		initialItems := []StashItem{
			{Section: "wpn_pm", Count: 1},
			{Section: "bandage", Count: 4},
		}
		initialJSON, _ := json.Marshal(initialItems)

		err = mgr.SaveStash(stashID, "l01_escape", 12.5, 1.0, -45.0, initialJSON)
		if err != nil {
			t.Fatalf("SaveStash failed: %v", err)
		}

		stash, err := mgr.GetStash(stashID)
		if err != nil {
			t.Fatalf("GetStash failed: %v", err)
		}
		if stash.StashID != stashID || stash.LevelName != "l01_escape" {
			t.Errorf("Unexpected stash data: %+v", stash)
		}
		if string(stash.Contents) != string(initialJSON) {
			t.Errorf("Expected contents %s, got %s", string(initialJSON), string(stash.Contents))
		}
	})

	t.Run("Nonexistent Stash Operations", func(t *testing.T) {
		db, err := database.Open(":memory:")
		if err != nil {
			t.Fatalf("Failed to open test db: %v", err)
		}
		mgr := NewStashManager(db)

		if _, err := mgr.GetStash(9999); !errors.Is(err, ErrStashNotFound) {
			t.Errorf("Expected ErrStashNotFound on GetStash, got %v", err)
		}
		if _, err := mgr.DepositItem(9999, "uuid-x", "l01_escape", "medkit", 1, 0); !errors.Is(err, ErrStashNotFound) {
			t.Errorf("Expected ErrStashNotFound on DepositItem, got %v", err)
		}
	})
}

func openStashTestDB(t *testing.T, uuid string) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("Failed to open test db: %v", err)
	}
	db.RawDB().SetMaxOpenConns(1)
	if err := db.AutoProvision(uuid, "hwid-"+uuid, "StashTest"); err != nil {
		t.Fatalf("AutoProvision failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Condition flows inventory -> stash -> inventory under the server-derived
// bucket at every hop.
func TestStashConditionRoundTripPreservesInventoryCondition(t *testing.T) {
	db := openStashTestDB(t, "uuid-cond")
	if _, err := db.RawDB().Exec(
		"INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, 'bandage', 2, 0.25)", "uuid-cond"); err != nil {
		t.Fatalf("seed inventory failed: %v", err)
	}
	mgr := NewStashManager(db)
	if err := mgr.SaveStash(902, "l01_escape", 0, 0, 0, []byte("[]")); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	// The client claims 100, but the inventory row is 25: the stash must store 25.
	bucket, err := mgr.DepositItem(902, "uuid-cond", "l01_escape", "bandage", 2, 100)
	if err != nil {
		t.Fatalf("DepositItem failed: %v", err)
	}
	if bucket != 25 {
		t.Fatalf("deposit bucket = %d, want 25 from the inventory row", bucket)
	}
	data, _ := mgr.GetStash(902)
	var stored []StashItem
	_ = json.Unmarshal(data.Contents, &stored)
	if len(stored) != 1 || stored[0].Condition != 25 {
		t.Fatalf("stash after deposit = %+v, want bucket 25", stored)
	}

	// Withdraw credits the stash bucket back as the inventory condition.
	bucket, err = mgr.WithdrawItem(902, "uuid-cond", "l01_escape", "bandage", 1, 25)
	if err != nil {
		t.Fatalf("WithdrawItem failed: %v", err)
	}
	if bucket != 25 {
		t.Fatalf("withdraw bucket = %d, want 25", bucket)
	}
	items, err := db.GetCharacterInventory("uuid-cond")
	if err != nil {
		t.Fatalf("GetCharacterInventory failed: %v", err)
	}
	if len(items) != 1 || items[0].ItemCount != 1 {
		t.Fatalf("inventory after withdraw = %+v, want one row with count 1", items)
	}
	if got := int(items[0].Condition*100 + 0.5); got != 25 {
		t.Fatalf("inventory condition = %d, want 25 (from the stash bucket)", got)
	}
}

// Withdrawing into an ammo-bearing single-object row adds rounds to
// ammo_current instead of creating item copies.
func TestStashWithdrawCreditsAmmoRounds(t *testing.T) {
	db := openStashTestDB(t, "uuid-ammo")
	if _, err := db.RawDB().Exec(
		"INSERT INTO character_inventory (client_uuid, item_section, item_count, condition, ammo_current) VALUES (?, 'wpn_pm', 1, 1.0, 30)", "uuid-ammo"); err != nil {
		t.Fatalf("seed inventory failed: %v", err)
	}
	mgr := NewStashManager(db)
	contents, _ := json.Marshal([]StashItem{{Section: "wpn_pm", Count: 5, Condition: 100}})
	if err := mgr.SaveStash(903, "l01_escape", 0, 0, 0, contents); err != nil {
		t.Fatalf("SaveStash failed: %v", err)
	}

	if _, err := mgr.WithdrawItem(903, "uuid-ammo", "l01_escape", "wpn_pm", 5, 100); err != nil {
		t.Fatalf("WithdrawItem failed: %v", err)
	}
	items, err := db.GetCharacterInventory("uuid-ammo")
	if err != nil {
		t.Fatalf("GetCharacterInventory failed: %v", err)
	}
	if len(items) != 1 || items[0].ItemCount != 1 || items[0].AmmoCurrent != 35 {
		t.Fatalf("ammo withdraw = %+v, want one object with ammo 35", items)
	}
}

// ValidateAccess rejects a non-owner on a personal stash and allows any
// requester on an ownerless world stash.
func TestValidateAccessRejectsForeignOwner(t *testing.T) {
	mgr := NewStashManager()
	owned := &database.StashRecord{LevelName: "l01_escape", OwnerUUID: "owner-uuid"}
	if err := mgr.ValidateAccess(owned, [3]float32{0, 0, 0}, "l01_escape", "", "intruder"); !errors.Is(err, ErrStashAccessDenied) {
		t.Fatalf("foreign owner access err = %v, want ErrStashAccessDenied", err)
	}
	if err := mgr.ValidateAccess(owned, [3]float32{0, 0, 0}, "l01_escape", "", "owner-uuid"); err != nil {
		t.Fatalf("owner access err = %v, want nil", err)
	}
	world := &database.StashRecord{LevelName: "l01_escape"}
	if err := mgr.ValidateAccess(world, [3]float32{0, 0, 0}, "l01_escape", "", "anyone"); err != nil {
		t.Fatalf("ownerless world stash err = %v, want nil", err)
	}
}
