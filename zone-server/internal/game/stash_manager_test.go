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

	t.Run("Save, Get, and Open Stash", func(t *testing.T) {
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

		sessionID := uint32(777)
		contents, err := mgr.OpenStash(stashID, sessionID)
		if err != nil {
			t.Fatalf("OpenStash failed: %v", err)
		}
		if string(contents) != string(initialJSON) {
			t.Errorf("Expected contents %s, got %s", string(initialJSON), string(contents))
		}

		openID, ok := mgr.GetOpenStash(sessionID)
		if !ok || openID != stashID {
			t.Errorf("Expected open stash %d, got %d (ok: %v)", stashID, openID, ok)
		}

		mgr.CloseStash(sessionID)
		_, ok = mgr.GetOpenStash(sessionID)
		if ok {
			t.Errorf("Expected stash to be closed for session %d", sessionID)
		}
	})

	t.Run("ModifyStashItem", func(t *testing.T) {
		db, err := database.Open(":memory:")
		if err != nil {
			t.Fatalf("Failed to open test db: %v", err)
		}
		mgr := NewStashManager(db)

		stashID := uint32(202)
		if err := mgr.SaveStash(stashID, "l02_garbage", 0, 0, 0, []byte("[]")); err != nil {
			t.Fatalf("SaveStash failed: %v", err)
		}

		// 1. Add new item
		if err := mgr.ModifyStashItem(stashID, "ammo_9x18_fmj", 10); err != nil {
			t.Fatalf("ModifyStashItem (add) failed: %v", err)
		}

		stash, _ := mgr.GetStash(stashID)
		var items []StashItem
		_ = json.Unmarshal(stash.Contents, &items)
		if len(items) != 1 || items[0].Section != "ammo_9x18_fmj" || items[0].Count != 10 {
			t.Fatalf("Expected 10 ammo_9x18_fmj, got %+v", items)
		}

		// 2. Add count to existing item
		if err := mgr.ModifyStashItem(stashID, "ammo_9x18_fmj", 5); err != nil {
			t.Fatalf("ModifyStashItem (add more) failed: %v", err)
		}
		stash, _ = mgr.GetStash(stashID)
		_ = json.Unmarshal(stash.Contents, &items)
		if items[0].Count != 15 {
			t.Fatalf("Expected 15 ammo_9x18_fmj, got %d", items[0].Count)
		}

		// 3. Deduct partial count
		if err := mgr.ModifyStashItem(stashID, "ammo_9x18_fmj", -5); err != nil {
			t.Fatalf("ModifyStashItem (deduct) failed: %v", err)
		}
		stash, _ = mgr.GetStash(stashID)
		_ = json.Unmarshal(stash.Contents, &items)
		if items[0].Count != 10 {
			t.Fatalf("Expected 10 ammo_9x18_fmj, got %d", items[0].Count)
		}

		// 4. Deduct entire count (removes item)
		if err := mgr.ModifyStashItem(stashID, "ammo_9x18_fmj", -10); err != nil {
			t.Fatalf("ModifyStashItem (remove) failed: %v", err)
		}
		stash, _ = mgr.GetStash(stashID)
		_ = json.Unmarshal(stash.Contents, &items)
		if len(items) != 0 {
			t.Fatalf("Expected 0 items after full deduction, got %+v", items)
		}

		// 5. Deduct from nonexistent item -> error
		if err := mgr.ModifyStashItem(stashID, "ammo_9x18_fmj", -1); !errors.Is(err, ErrItemNotFoundStash) {
			t.Errorf("Expected ErrItemNotFoundStash, got %v", err)
		}

		// 6. Deduct more than available -> error
		_ = mgr.ModifyStashItem(stashID, "medkit", 2)
		if err := mgr.ModifyStashItem(stashID, "medkit", -5); !errors.Is(err, ErrInsufficientCount) {
			t.Errorf("Expected ErrInsufficientCount, got %v", err)
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
		if _, err := mgr.OpenStash(9999, 1); !errors.Is(err, ErrStashNotFound) {
			t.Errorf("Expected ErrStashNotFound on OpenStash, got %v", err)
		}
		if err := mgr.ModifyStashItem(9999, "medkit", 1); !errors.Is(err, ErrStashNotFound) {
			t.Errorf("Expected ErrStashNotFound on ModifyStashItem, got %v", err)
		}
	})
}
