package game

import (
	"encoding/json"

	"zone-online/zone-server/internal/ai"
	"zone-online/zone-server/internal/database"
)

// defaultAISquads is the fallback l01_escape spawn set inserted when the
// ai_squads table is empty. Coordinates sit around the Cordon rookie village
// (safe-zone centre -211.3, -20.2, -145.8) but at least ~245 m from the world
// origin so DB-backed unit tests whose ad-hoc sessions sit at (0,0,0) never
// trip the 220 m AI replication radius. Each row is one squads-worth of
// conceptual members (three stalkers or bandits); wave A replicates a single
// leader puppet per squad. Patrol radius is generated as a 12-point circle at
// registration time (see ai.RegisterPuppet).
//
// Walking speed 1.5 m/s is the patrol default; run speed 3.0 m/s is reserved
// for the optional run transition (ai.SetPuppetRun) and is unused by the
// seeded data.
func defaultAISquads() []ai.PuppetDef {
	return []ai.PuppetDef{
		{
			Label:        "Cordon Patrol Alpha",
			Section:      "sim_default_stalker_0",
			Faction:      "stalker",
			Level:        "l01_escape",
			Spawn:        [3]float32{-281.0, -20.0, -189.0},
			PatrolRadius: 32.0,
			WalkSpeed:    1.5,
			RunSpeed:     3.0,
		},
		{
			Label:        "Cordon Patrol Bravo",
			Section:      "sim_default_stalker_1",
			Faction:      "stalker",
			Level:        "l01_escape",
			Spawn:        [3]float32{-247.0, -21.0, -121.0},
			PatrolRadius: 28.0,
			WalkSpeed:    1.5,
			RunSpeed:     3.0,
		},
		{
			Label:        "Cordon Bandit Raid",
			Section:      "sim_default_bandit_0",
			Faction:      "bandit",
			Level:        "l01_escape",
			Spawn:        [3]float32{-156.0, -19.0, -207.0},
			PatrolRadius: 30.0,
			WalkSpeed:    1.5,
			RunSpeed:     3.0,
		},
		{
			Label:        "Cordon Bandit Outpost",
			Section:      "sim_default_bandit_1",
			Faction:      "bandit",
			Level:        "l01_escape",
			Spawn:        [3]float32{-300.0, -22.0, -110.0},
			PatrolRadius: 40.0,
			WalkSpeed:    1.5,
			RunSpeed:     3.0,
		},
	}
}

// SeedAISquads inserts the default l01_escape spawn set when ai_squads is
// empty. The seed is idempotent: a table with any rows (operator-managed or a
// previous seed) is left untouched.
func SeedAISquads(db *database.DB) error {
	if db == nil {
		return nil
	}
	var count int
	if err := db.RawDB().QueryRow("SELECT COUNT(*) FROM ai_squads").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	tx, err := db.RawDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO ai_squads
		(level_name, section, faction, pos_x, pos_y, pos_z, patrol_path, is_online, label, patrol_radius, walk_speed, run_speed)
		VALUES (?, ?, ?, ?, ?, ?, NULL, 0, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, def := range defaultAISquads() {
		if _, err := stmt.Exec(def.Level, def.Section, def.Faction, def.Spawn[0], def.Spawn[1], def.Spawn[2],
			def.Label, def.PatrolRadius, def.WalkSpeed, def.RunSpeed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// loadAISquads reads every ai_squads row into a PuppetDef. patrol_path, when
// present, must be a JSON array of [x,y,z] triples; an unparsable value falls
// back to the generated circular circuit.
func loadAISquads(db *database.DB) ([]ai.PuppetDef, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.RawDB().Query(`SELECT squad_id, level_name, section, faction, pos_x, pos_y, pos_z,
		COALESCE(patrol_path, ''), COALESCE(label, ''), patrol_radius, walk_speed, run_speed
		FROM ai_squads ORDER BY squad_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var defs []ai.PuppetDef
	for rows.Next() {
		var (
			def        ai.PuppetDef
			patrolPath string
		)
		if err := rows.Scan(&def.DBID, &def.Level, &def.Section, &def.Faction,
			&def.Spawn[0], &def.Spawn[1], &def.Spawn[2],
			&patrolPath, &def.Label, &def.PatrolRadius, &def.WalkSpeed, &def.RunSpeed); err != nil {
			return nil, err
		}
		if def.Label == "" {
			def.Label = def.Section
		}
		def.Loop = parsePatrolPath(patrolPath)
		defs = append(defs, def)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return defs, nil
}

// parsePatrolPath decodes a JSON array of [x,y,z] triples. Invalid or empty
// paths return nil so RegisterPuppet generates a circle from the spawn point.
func parsePatrolPath(raw string) []ai.Waypoint {
	if raw == "" {
		return nil
	}
	var triples [][3]float32
	if err := json.Unmarshal([]byte(raw), &triples); err != nil || len(triples) < 2 {
		return nil
	}
	loop := make([]ai.Waypoint, 0, len(triples))
	for _, t := range triples {
		loop = append(loop, ai.Waypoint{X: t[0], Y: t[1], Z: t[2]})
	}
	return loop
}
