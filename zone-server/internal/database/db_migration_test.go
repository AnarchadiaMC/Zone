package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// createLegacyProfileDB builds a pre-versioning database on disk: the old
// characters schema (profile_rev present but no dead/created_at columns) and a
// character_inventory table without item_count, so Open must both ALTER and
// apply the one-time promotion/backfill.
func createLegacyProfileDB(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw sql.Open failed: %v", err)
	}
	defer raw.Close()
	stmts := []string{
		`CREATE TABLE accounts (
			client_uuid TEXT PRIMARY KEY,
			hwid_hash TEXT NOT NULL,
			nickname TEXT NOT NULL UNIQUE,
			banned INTEGER NOT NULL DEFAULT 0,
			ban_reason TEXT,
			created_at INTEGER NOT NULL,
			last_seen INTEGER NOT NULL
		)`,
		`CREATE TABLE characters (
			client_uuid TEXT PRIMARY KEY,
			level_name TEXT NOT NULL DEFAULT 'l01_escape',
			pos_x REAL NOT NULL DEFAULT 0,
			pos_y REAL NOT NULL DEFAULT 0,
			pos_z REAL NOT NULL DEFAULT 0,
			yaw REAL NOT NULL DEFAULT 0,
			faction TEXT NOT NULL DEFAULT 'stalker',
			health REAL NOT NULL DEFAULT 1.0,
			bleeding REAL NOT NULL DEFAULT 0.0,
			radiation REAL NOT NULL DEFAULT 0.0,
			economy_tier INTEGER NOT NULL DEFAULT 1,
			rank TEXT NOT NULL DEFAULT 'novice',
			reputation INTEGER NOT NULL DEFAULT 0,
			rubles INTEGER NOT NULL DEFAULT 5000,
			play_time_sec INTEGER NOT NULL DEFAULT 0,
			profile_rev INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE character_inventory (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			client_uuid TEXT NOT NULL,
			item_section TEXT NOT NULL,
			condition REAL NOT NULL DEFAULT 1.0,
			ammo_current INTEGER NOT NULL DEFAULT 0,
			addon_flags INTEGER NOT NULL DEFAULT 0,
			slot INTEGER NOT NULL DEFAULT -1,
			grid_x INTEGER NOT NULL DEFAULT 0,
			grid_y INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO accounts (client_uuid, hwid_hash, nickname, created_at, last_seen)
			VALUES ('uuid-placeholder', 'hwid-p', 'Placeholder', 1, 2)`,
		`INSERT INTO characters (client_uuid, profile_rev, updated_at) VALUES ('uuid-placeholder', 0, 200)`,
		`INSERT INTO accounts (client_uuid, hwid_hash, nickname, created_at, last_seen)
			VALUES ('uuid-legacy', 'hwid-l', 'Legacy', 1, 2)`,
		`INSERT INTO characters (client_uuid, profile_rev, updated_at) VALUES ('uuid-legacy', 0, 200)`,
		// The inventory row is what proves legacy provenance once the backfill
		// has set created_at = updated_at.
		`INSERT INTO character_inventory (client_uuid, item_section) VALUES ('uuid-legacy', 'bandage')`,
	}
	for _, stmt := range stmts {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("legacy schema exec failed: %v\nstmt: %s", err, stmt)
		}
	}
}

func TestMigrateProfileRevKeepsAutoProvisionPlaceholders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile_rev.db")
	createLegacyProfileDB(t, path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	placeholder, err := db.LoadCharacter("uuid-placeholder")
	if err != nil {
		t.Fatalf("LoadCharacter(placeholder) failed: %v", err)
	}
	if placeholder.ProfileRev != 0 {
		t.Fatalf("AutoProvision placeholder was promoted: profile_rev=%d, want 0", placeholder.ProfileRev)
	}

	legacy, err := db.LoadCharacter("uuid-legacy")
	if err != nil {
		t.Fatalf("LoadCharacter(legacy) failed: %v", err)
	}
	if legacy.ProfileRev != 1 {
		t.Fatalf("persisted legacy character profile_rev=%d, want 1", legacy.ProfileRev)
	}

	if version, err := schemaVersion(db.RawDB()); err != nil || version != schemaVersionLatest {
		t.Fatalf("schema_version = %d err=%v, want %d", version, err, schemaVersionLatest)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	placeholder, err = reopened.LoadCharacter("uuid-placeholder")
	if err != nil {
		t.Fatalf("LoadCharacter(placeholder) after reopen failed: %v", err)
	}
	if placeholder.ProfileRev != 0 {
		t.Fatalf("placeholder promoted on reopen: profile_rev=%d, want 0", placeholder.ProfileRev)
	}

	// Migration must run exactly once: a legacy-shaped row inserted after the
	// stamped version is not promoted by a later boot.
	if _, err := reopened.RawDB().Exec(`INSERT INTO accounts (client_uuid, hwid_hash, nickname, created_at, last_seen)
		VALUES ('uuid-late', 'hwid-late', 'Late', 1, 2)`); err != nil {
		t.Fatalf("late account insert failed: %v", err)
	}
	if _, err := reopened.RawDB().Exec(`INSERT INTO characters (client_uuid, profile_rev, created_at, updated_at)
		VALUES ('uuid-late', 0, 100, 200)`); err != nil {
		t.Fatalf("late character insert failed: %v", err)
	}
	if _, err := reopened.RawDB().Exec(`INSERT INTO character_inventory (client_uuid, item_section) VALUES ('uuid-late', 'bandage')`); err != nil {
		t.Fatalf("late inventory insert failed: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("second reopen failed: %v", err)
	}
	defer again.Close()
	late, err := again.LoadCharacter("uuid-late")
	if err != nil {
		t.Fatalf("LoadCharacter(late) failed: %v", err)
	}
	if late.ProfileRev != 0 {
		t.Fatalf("migration ran twice: late legacy row promoted to profile_rev=%d", late.ProfileRev)
	}
}
