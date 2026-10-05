package game

import (
	"strings"
	"sync"

	"zone-online/zone-server/internal/database"
)

// factionRelationTable is the authoritative in-memory relations matrix. It
// starts from the embedded Go defaults so DB-less runs and tests work, and is
// replaced by LoadFactionRelations when a database is available.
var factionRelationTable = struct {
	sync.RWMutex
	relations map[string]map[string]int
}{relations: relationMapFromDefaults()}

func relationMapFromDefaults() map[string]map[string]int {
	out := make(map[string]map[string]int)
	for _, r := range database.DefaultFactionRelations() {
		a := normalizeFaction(r.Faction)
		b := normalizeFaction(r.Other)
		if a == "" || b == "" {
			continue
		}
		if out[a] == nil {
			out[a] = make(map[string]int)
		}
		out[a][b] = r.Value
		if out[b] == nil {
			out[b] = make(map[string]int)
		}
		out[b][a] = r.Value
	}
	return out
}

// LoadFactionRelations seeds the embedded matrix when the table is empty and
// loads the persisted table into memory so operator edits apply after restart.
func LoadFactionRelations(db *database.DB) error {
	if db == nil {
		return nil
	}
	relations, err := db.LoadFactionRelations()
	if err != nil {
		return err
	}
	factionRelationTable.Lock()
	factionRelationTable.relations = relations
	factionRelationTable.Unlock()
	return nil
}

// normalizeFaction lowercases a faction id and strips the optional "actor_"
// prefix used by vanilla community ids (actor_stalker == stalker).
func normalizeFaction(faction string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(faction)), "actor_")
}

// RelationBetween returns the community relation value between two factions:
// -2000 enemy, 0 neutral/unknown, 300 ally. Same-faction pairs are not stored
// and report 0; callers use sameFaction to reject friendly fire.
func RelationBetween(a, b string) int {
	a = normalizeFaction(a)
	b = normalizeFaction(b)
	factionRelationTable.RLock()
	defer factionRelationTable.RUnlock()
	if m, ok := factionRelationTable.relations[a]; ok {
		if v, ok := m[b]; ok {
			return v
		}
	}
	return 0
}

// sameFaction reports whether two faction strings normalize to the same
// non-empty faction.
func sameFaction(a, b string) bool {
	a = normalizeFaction(a)
	b = normalizeFaction(b)
	if a == "" || b == "" {
		return false
	}
	return a == b
}
