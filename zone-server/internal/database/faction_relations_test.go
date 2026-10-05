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
