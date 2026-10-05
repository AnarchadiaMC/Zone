package database

import (
	"path/filepath"
	"testing"
)

func TestFactionRelationsSeedAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "faction_relations.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	relations, err := db.LoadFactionRelations()
	if err != nil {
		t.Fatalf("LoadFactionRelations failed: %v", err)
	}
	if got := relations["stalker"]["bandit"]; got != -2000 {
		t.Errorf("stalker->bandit = %d, want -2000", got)
	}
	if got := relations["dolg"]["army"]; got != 0 {
		t.Errorf("dolg->army = %d, want 0", got)
	}
	if got := relations["monolith"]["greh"]; got != 300 {
		t.Errorf("monolith->greh = %d, want 300", got)
	}
	if got := relations["bandit"]["renegade"]; got != 300 {
		t.Errorf("bandit->renegade = %d, want 300", got)
	}

	// Symmetry: every stored pair must match its reverse.
	for faction, others := range relations {
		for other, value := range others {
			if got := relations[other][faction]; got != value {
				t.Fatalf("asymmetric relation %s<->%s: %d vs %d", faction, other, value, got)
			}
		}
	}

	// Operator edits survive re-open; seeding is a no-op once populated.
	if _, err := db.RawDB().Exec(`UPDATE faction_relations SET value = -1000 WHERE faction = 'stalker' AND other = 'dolg'`); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer reopened.Close()
	relations, err = reopened.LoadFactionRelations()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if got := relations["stalker"]["dolg"]; got != -1000 {
		t.Errorf("operator edit lost: stalker->dolg = %d, want -1000", got)
	}
}

// Seeding repairs a missing canonical row instead of bailing out because the
// table is non-empty.
func TestSeedFactionRelationsRestoresDeletedRow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "faction_seed_repair.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if _, err := db.LoadFactionRelations(); err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
	if _, err := db.RawDB().Exec(`DELETE FROM faction_relations WHERE faction='stalker' AND other='dolg'`); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if err := db.SeedFactionRelations(); err != nil {
		t.Fatalf("SeedFactionRelations failed: %v", err)
	}
	var value int
	if err := db.RawDB().QueryRow(`SELECT value FROM faction_relations WHERE faction='stalker' AND other='dolg'`).Scan(&value); err != nil {
		t.Fatalf("deleted canonical row was not restored: %v", err)
	}
	if value != 0 {
		t.Errorf("restored stalker->dolg = %d, want 0", value)
	}

	// Operator edits are still never overwritten by the reseed.
	if _, err := db.RawDB().Exec(`UPDATE faction_relations SET value=-999 WHERE faction='monolith' AND other='greh'`); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if err := db.SeedFactionRelations(); err != nil {
		t.Fatalf("reseed failed: %v", err)
	}
	if err := db.RawDB().QueryRow(`SELECT value FROM faction_relations WHERE faction='monolith' AND other='greh'`).Scan(&value); err != nil {
		t.Fatalf("read edited row: %v", err)
	}
	if value != -999 {
		t.Errorf("reseed overwrote operator edit: monolith->greh = %d, want -999", value)
	}
}

// Rows stored raw (actor_ prefix, mixed case) are normalized on load so the
// in-memory lookup matches them.
func TestLoadFactionRelationsNormalizesRawRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "faction_normalize.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if _, err := db.RawDB().Exec(`DELETE FROM faction_relations`); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, err := db.RawDB().Exec(`INSERT INTO faction_relations (faction, other, value) VALUES ('actor_Stalker', 'Actor_Bandit', -2000)`); err != nil {
		t.Fatalf("raw insert failed: %v", err)
	}

	relations, err := db.LoadFactionRelations()
	if err != nil {
		t.Fatalf("LoadFactionRelations failed: %v", err)
	}
	if _, ok := relations["actor_stalker"]; ok {
		t.Error("raw actor_ key leaked into the loaded relation map")
	}
	if got := relations["stalker"]["bandit"]; got != -2000 {
		t.Errorf("normalized stalker->bandit = %d, want -2000", got)
	}
}

// Seeded keys are normalized (lowercase, no actor_ prefix).
func TestSeedFactionRelationsWritesNormalizedKeys(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "faction_seed_norm.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.SeedFactionRelations(); err != nil {
		t.Fatalf("SeedFactionRelations failed: %v", err)
	}
	var bad int
	if err := db.RawDB().QueryRow(
		`SELECT COUNT(*) FROM faction_relations WHERE faction <> lower(faction) OR other <> lower(other) OR faction LIKE 'actor\_%' ESCAPE '\'`).Scan(&bad); err != nil {
		t.Fatalf("normalization query failed: %v", err)
	}
	if bad != 0 {
		t.Errorf("seed wrote %d unnormalized rows, want 0", bad)
	}
}

func TestNormalizeFactionKey(t *testing.T) {
	if got := normalizeFactionKey(" actor_Stalker "); got != "stalker" {
		t.Errorf("normalizeFactionKey = %q, want stalker", got)
	}
	if got := normalizeFactionKey("DOLG"); got != "dolg" {
		t.Errorf("normalizeFactionKey = %q, want dolg", got)
	}
}
