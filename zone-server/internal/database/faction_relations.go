package database

// FactionRelation is one (faction, other) -> value cell of the faction
// relations matrix. Values match vanilla Anomaly game_relations.ltx:
// -2000 enemy, 0 neutral, 300 ally (2000 for the two allied coalition
// cores, monolith<->monolith is implicit and not stored).
type FactionRelation struct {
	Faction string
	Other   string
	Value   int
}

// defaultFactionRelationPairs is the canonical symmetric matrix derived from
// the vanilla Anomaly [communities_relations] table for the selectable
// factions (stalker, bandit, dolg, freedom, csky, ecolog, killer, army,
// monolith, renegade, greh, isg) plus zombied, which AllowedFactions also
// accepts. Each pair is stored in both directions by DefaultFactionRelations.
var defaultFactionRelationPairs = []struct {
	a, b string
	v    int
}{
	{"stalker", "bandit", -2000},
	{"stalker", "dolg", 0},
	{"stalker", "freedom", 0},
	{"stalker", "csky", 0},
	{"stalker", "ecolog", 0},
	{"stalker", "killer", -2000},
	{"stalker", "army", -2000},
	{"stalker", "monolith", -2000},
	{"stalker", "renegade", -2000},
	{"stalker", "greh", -2000},
	{"stalker", "zombied", -2000},
	{"stalker", "isg", -2000},

	{"bandit", "dolg", -2000},
	{"bandit", "freedom", 0},
	{"bandit", "csky", -2000},
	{"bandit", "ecolog", -2000},
	{"bandit", "killer", 0},
	{"bandit", "army", -2000},
	{"bandit", "monolith", -2000},
	{"bandit", "renegade", 300},
	{"bandit", "greh", -2000},
	{"bandit", "zombied", -2000},
	{"bandit", "isg", -2000},

	{"dolg", "freedom", -2000},
	{"dolg", "csky", 0},
	{"dolg", "ecolog", 0},
	{"dolg", "killer", -2000},
	{"dolg", "army", 0},
	{"dolg", "monolith", -2000},
	{"dolg", "renegade", -2000},
	{"dolg", "greh", -2000},
	{"dolg", "zombied", -2000},
	{"dolg", "isg", -2000},

	{"freedom", "csky", 0},
	{"freedom", "ecolog", 0},
	{"freedom", "killer", 0},
	{"freedom", "army", -2000},
	{"freedom", "monolith", -2000},
	{"freedom", "renegade", -2000},
	{"freedom", "greh", -2000},
	{"freedom", "zombied", -2000},
	{"freedom", "isg", -2000},

	{"csky", "ecolog", 0},
	{"csky", "killer", 0},
	{"csky", "army", -2000},
	{"csky", "monolith", -2000},
	{"csky", "renegade", -2000},
	{"csky", "greh", -2000},
	{"csky", "zombied", -2000},
	{"csky", "isg", -2000},

	{"ecolog", "killer", 0},
	{"ecolog", "army", 0},
	{"ecolog", "monolith", -2000},
	{"ecolog", "renegade", -2000},
	{"ecolog", "greh", -2000},
	{"ecolog", "zombied", -2000},
	{"ecolog", "isg", -2000},

	{"killer", "army", -2000},
	{"killer", "monolith", -2000},
	{"killer", "renegade", -2000},
	{"killer", "greh", -2000},
	{"killer", "zombied", -2000},
	{"killer", "isg", 0},

	{"army", "monolith", -2000},
	{"army", "renegade", -2000},
	{"army", "greh", -2000},
	{"army", "zombied", -2000},
	{"army", "isg", -2000},

	{"monolith", "renegade", -2000},
	{"monolith", "greh", 300},
	{"monolith", "zombied", 300},
	{"monolith", "isg", -2000},

	{"renegade", "greh", -2000},
	{"renegade", "zombied", -2000},
	{"renegade", "isg", -2000},

	{"greh", "zombied", 300},
	{"greh", "isg", -2000},

	{"isg", "zombied", -2000},
}

// DefaultFactionRelations expands the canonical pairs in both directions.
func DefaultFactionRelations() []FactionRelation {
	out := make([]FactionRelation, 0, len(defaultFactionRelationPairs)*2)
	for _, p := range defaultFactionRelationPairs {
		out = append(out, FactionRelation{Faction: p.a, Other: p.b, Value: p.v})
		out = append(out, FactionRelation{Faction: p.b, Other: p.a, Value: p.v})
	}
	return out
}

// SeedFactionRelations inserts the built-in matrix when the table is empty.
// Operator-edited rows are never overwritten.
func (d *DB) SeedFactionRelations() error {
	var count int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM faction_relations").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO faction_relations (faction, other, value) VALUES (?, ?, ?) ON CONFLICT(faction, other) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range DefaultFactionRelations() {
		if _, err := stmt.Exec(r.Faction, r.Other, r.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadFactionRelations seeds defaults when empty, then returns the full
// symmetric matrix keyed by faction.
func (d *DB) LoadFactionRelations() (map[string]map[string]int, error) {
	if err := d.SeedFactionRelations(); err != nil {
		return nil, err
	}
	rows, err := d.db.Query("SELECT faction, other, value FROM faction_relations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]map[string]int)
	for rows.Next() {
		var faction, other string
		var value int
		if err := rows.Scan(&faction, &other, &value); err != nil {
			return nil, err
		}
		if out[faction] == nil {
			out[faction] = make(map[string]int)
		}
		out[faction][other] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
