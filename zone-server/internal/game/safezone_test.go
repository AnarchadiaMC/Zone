package game

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSafeZone(t *testing.T) {
	sz := CheckSafeZone(-211.3, -20.2, -145.8, "l01_escape")
	if sz == nil {
		t.Fatal("Expected safe zone, got nil")
	}
	if sz.ZoneID != "sz_cordon_rookie" {
		t.Fatalf("Expected sz_cordon_rookie, got %s", sz.ZoneID)
	}
}

func TestSafeZone_AllCanonicalZones(t *testing.T) {
	// Canonical 12 per GEMINI §13.1 (was 8; Dead City / Swamp / Zaton /
	// Jupiter added so CLIENT-declared safe zones are server-enforced).
	expectedZones := []string{
		"sz_cordon_rookie",
		"sz_cordon_farm",
		"sz_garbage_flea",
		"sz_rostok_bar",
		"sz_darkvalley_base",
		"sz_agroprom_camp",
		"sz_warehouses_base",
		"sz_yantar_bunker",
		"sz_deadcity_base",
		"sz_swamp_clearsky",
		"sz_zaton_skadovsk",
		"sz_jupiter_yanov",
	}

	if len(defaultSafeZones) != len(expectedZones) {
		t.Fatalf("expected %d default safe zones, got %d", len(expectedZones), len(defaultSafeZones))
	}

	for i, expectedID := range expectedZones {
		sz := defaultSafeZones[i]
		if sz.ZoneID != expectedID {
			t.Errorf("zone index %d: expected %s, got %s", i, expectedID, sz.ZoneID)
		}

		// 1. Test exact center
		res := CheckSafeZone(sz.CenterX, sz.CenterY, sz.CenterZ, sz.LevelName)
		if res == nil {
			t.Fatalf("[%s] expected center to be inside safe zone, got nil", sz.ZoneID)
		}
		if res.ZoneID != sz.ZoneID {
			t.Fatalf("[%s] expected ZoneID %s, got %s", sz.ZoneID, sz.ZoneID, res.ZoneID)
		}

		// 2. Test interior point inside radius and height
		resInterior := CheckSafeZone(
			sz.CenterX+sz.Radius*0.5,
			sz.CenterY+sz.Height*0.2,
			sz.CenterZ+sz.Radius*0.5,
			sz.LevelName,
		)
		if resInterior == nil || resInterior.ZoneID != sz.ZoneID {
			t.Fatalf("[%s] expected interior point to be inside safe zone, got %v", sz.ZoneID, resInterior)
		}

		// 3. Test near boundary inside radius and height (0.95*R and 0.95*(H/2))
		resNearBoundary := CheckSafeZone(
			sz.CenterX+sz.Radius*0.95,
			sz.CenterY+(sz.Height/2.0)*0.95,
			sz.CenterZ,
			sz.LevelName,
		)
		if resNearBoundary == nil || resNearBoundary.ZoneID != sz.ZoneID {
			t.Fatalf("[%s] expected point inside radius/height boundary to be inside safe zone, got %v", sz.ZoneID, resNearBoundary)
		}
	}
}

func TestSafeZone_OutsideHorizontalRadius(t *testing.T) {
	for _, sz := range defaultSafeZones {
		// Just outside radius on +X
		outsideX := CheckSafeZone(sz.CenterX+sz.Radius+0.5, sz.CenterY, sz.CenterZ, sz.LevelName)
		if outsideX != nil {
			t.Fatalf("[%s] expected nil for point outside +X radius, got %s", sz.ZoneID, outsideX.ZoneID)
		}

		// Just outside radius on -Z
		outsideZ := CheckSafeZone(sz.CenterX, sz.CenterY, sz.CenterZ-sz.Radius-0.5, sz.LevelName)
		if outsideZ != nil {
			t.Fatalf("[%s] expected nil for point outside -Z radius, got %s", sz.ZoneID, outsideZ.ZoneID)
		}

		// Diagonal outside radius: (0.75^2 + 0.75^2 = 1.125 > 1.0)
		outsideDiag := CheckSafeZone(
			sz.CenterX+sz.Radius*0.75,
			sz.CenterY,
			sz.CenterZ+sz.Radius*0.75,
			sz.LevelName,
		)
		if outsideDiag != nil {
			t.Fatalf("[%s] expected nil for diagonal outside radius, got %s", sz.ZoneID, outsideDiag.ZoneID)
		}
	}
}

func TestSafeZone_OutsideVerticalBounds(t *testing.T) {
	for _, sz := range defaultSafeZones {
		halfH := sz.Height / 2.0

		// Above safe zone ceiling
		above := CheckSafeZone(sz.CenterX, sz.CenterY+halfH+0.5, sz.CenterZ, sz.LevelName)
		if above != nil {
			t.Fatalf("[%s] expected nil for point above ceiling, got %s", sz.ZoneID, above.ZoneID)
		}

		// Below safe zone floor
		below := CheckSafeZone(sz.CenterX, sz.CenterY-halfH-0.5, sz.CenterZ, sz.LevelName)
		if below != nil {
			t.Fatalf("[%s] expected nil for point below floor, got %s", sz.ZoneID, below.ZoneID)
		}
	}
}

func TestSafeZone_WrongLevel(t *testing.T) {
	for _, sz := range defaultSafeZones {
		// Matching coordinates but wrong level name
		resWrongLevel := CheckSafeZone(sz.CenterX, sz.CenterY, sz.CenterZ, "l99_invalid_level")
		if resWrongLevel != nil {
			t.Fatalf("[%s] expected nil for wrong level, got %s", sz.ZoneID, resWrongLevel.ZoneID)
		}

		// Empty level name
		resEmptyLevel := CheckSafeZone(sz.CenterX, sz.CenterY, sz.CenterZ, "")
		if resEmptyLevel != nil {
			t.Fatalf("[%s] expected nil for empty level, got %s", sz.ZoneID, resEmptyLevel.ZoneID)
		}
	}
}

func TestSafeZone_SeedSafeZones(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite db: %v", err)
	}
	defer db.Close()

	// 1. Seed without table should fail
	err = SeedSafeZones(db)
	if err == nil {
		t.Fatal("expected SeedSafeZones to fail when safe_zones table does not exist")
	}

	// 2. Create schema
	schema := `
	CREATE TABLE IF NOT EXISTS safe_zones (
		zone_id TEXT PRIMARY KEY,
		level_name TEXT NOT NULL,
		shape TEXT NOT NULL DEFAULT 'cylinder',
		center_x REAL NOT NULL,
		center_y REAL NOT NULL,
		center_z REAL NOT NULL,
		radius REAL NOT NULL,
		height REAL NOT NULL DEFAULT 20.0,
		description TEXT
	);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to create safe_zones table: %v", err)
	}

	// 3. Seed safe zones
	if err := SeedSafeZones(db); err != nil {
		t.Fatalf("SeedSafeZones failed: %v", err)
	}

	// 4. Verify count matches defaultSafeZones
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM safe_zones").Scan(&count); err != nil {
		t.Fatalf("failed to query safe_zones count: %v", err)
	}
	if count != len(defaultSafeZones) {
		t.Fatalf("expected %d safe zones in DB, got %d", len(defaultSafeZones), count)
	}

	// 5. Verify all default zones exist in DB with correct values
	for _, sz := range defaultSafeZones {
		var levelName, shape string
		var cx, cy, cz, r, h float32
		row := db.QueryRow(
			"SELECT level_name, shape, center_x, center_y, center_z, radius, height FROM safe_zones WHERE zone_id = ?",
			sz.ZoneID,
		)
		if err := row.Scan(&levelName, &shape, &cx, &cy, &cz, &r, &h); err != nil {
			t.Fatalf("failed to read seeded zone %s: %v", sz.ZoneID, err)
		}
		if levelName != sz.LevelName || shape != "cylinder" || cx != sz.CenterX || cy != sz.CenterY || cz != sz.CenterZ || r != sz.Radius || h != sz.Height {
			t.Fatalf("seeded zone %s mismatch: got level=%s shape=%s pos=(%v,%v,%v) r=%v h=%v",
				sz.ZoneID, levelName, shape, cx, cy, cz, r, h)
		}
	}

	// 6. Test idempotency: calling SeedSafeZones again should succeed with ON CONFLICT DO NOTHING
	if err := SeedSafeZones(db); err != nil {
		t.Fatalf("second SeedSafeZones call failed: %v", err)
	}
	var countAfter int
	if err := db.QueryRow("SELECT COUNT(*) FROM safe_zones").Scan(&countAfter); err != nil {
		t.Fatalf("failed to query count after second seed: %v", err)
	}
	if countAfter != len(defaultSafeZones) {
		t.Fatalf("expected count %d after idempotent re-seed, got %d", len(defaultSafeZones), countAfter)
	}
}
