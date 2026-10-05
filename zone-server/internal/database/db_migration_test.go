package database

import (
	"path/filepath"
	"testing"
)

func TestMigrateProfileRevKeepsAutoProvisionPlaceholders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile_rev.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if err := db.AutoProvision("uuid-placeholder", "hwid-placeholder", "Placeholder"); err != nil {
		t.Fatalf("AutoProvision failed: %v", err)
	}
	if err := db.AutoProvision("uuid-real", "hwid-real", "RealStalker"); err != nil {
		t.Fatalf("AutoProvision failed: %v", err)
	}
	if err := db.CreateCharacter("uuid-real", "stalker", ""); err != nil {
		t.Fatalf("CreateCharacter failed: %v", err)
	}
	// Legacy genuinely-created character: profile_rev=0 but persisted after
	// creation (updated_at advanced beyond created_at).
	if _, err := db.RawDB().Exec(`INSERT INTO accounts (client_uuid, hwid_hash, nickname, created_at, last_seen)
		VALUES ('uuid-legacy', 'hwid-legacy', 'Legacy', 100, 200)`); err != nil {
		t.Fatalf("legacy account insert failed: %v", err)
	}
	if _, err := db.RawDB().Exec(`INSERT INTO characters (client_uuid, profile_rev, created_at, updated_at, faction)
		VALUES ('uuid-legacy', 0, 100, 200, 'dolg')`); err != nil {
		t.Fatalf("legacy insert failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer reopened.Close()

	placeholder, err := reopened.LoadCharacter("uuid-placeholder")
	if err != nil {
		t.Fatalf("LoadCharacter(placeholder) failed: %v", err)
	}
	if placeholder.ProfileRev != 0 {
		t.Fatalf("AutoProvision placeholder was promoted: profile_rev=%d, want 0", placeholder.ProfileRev)
	}

	real, err := reopened.LoadCharacter("uuid-real")
	if err != nil {
		t.Fatalf("LoadCharacter(real) failed: %v", err)
	}
	if real.ProfileRev != 1 {
		t.Fatalf("created character profile_rev=%d, want 1", real.ProfileRev)
	}

	legacy, err := reopened.LoadCharacter("uuid-legacy")
	if err != nil {
		t.Fatalf("LoadCharacter(legacy) failed: %v", err)
	}
	if legacy.ProfileRev != 1 {
		t.Fatalf("persisted legacy character profile_rev=%d, want 1", legacy.ProfileRev)
	}
}
