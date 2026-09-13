package game

import (
	"database/sql"
	"math"
)

type SafeZone struct {
	ZoneID    string
	LevelName string
	CenterX   float32
	CenterY   float32
	CenterZ   float32
	Radius    float32
	Height    float32
}

var defaultSafeZones = []SafeZone{
	{"sz_cordon_rookie", "l01_escape", -211.3, -20.2, -145.8, 65.0, 20.0},
	{"sz_cordon_farm", "l01_escape", 32.4, 3.1, 150.2, 50.0, 20.0},
	{"sz_garbage_flea", "l02_garbage", -88.5, -0.5, -8.2, 45.0, 20.0},
	{"sz_rostok_bar", "l05_bar_rostok", 30.5, 0.5, 138.2, 120.0, 20.0},
	{"sz_darkvalley_base", "l04_darkvalley", 122.0, 1.2, -260.5, 80.0, 20.0},
	{"sz_agroprom_camp", "l03_agroprom", -150.2, 5.4, -40.0, 55.0, 20.0},
	{"sz_warehouses_base", "l07_military", -15.4, -5.2, 220.6, 110.0, 20.0},
	{"sz_yantar_bunker", "l08_yantar", 32.8, -11.5, -270.2, 45.0, 20.0},
}

func SeedSafeZones(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO safe_zones (zone_id, level_name, shape, center_x, center_y, center_z, radius, height)
		VALUES (?, ?, 'cylinder', ?, ?, ?, ?, ?)
		ON CONFLICT(zone_id) DO NOTHING
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, sz := range defaultSafeZones {
		_, err := stmt.Exec(sz.ZoneID, sz.LevelName, sz.CenterX, sz.CenterY, sz.CenterZ, sz.Radius, sz.Height)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func CheckSafeZone(x, y, z float32, level string) *SafeZone {
	for _, sz := range defaultSafeZones {
		if sz.LevelName != level {
			continue
		}
		
		dx := x - sz.CenterX
		dz := z - sz.CenterZ
		distSq := dx*dx + dz*dz

		if distSq <= sz.Radius*sz.Radius && math.Abs(float64(y-sz.CenterY)) <= float64(sz.Height)/2 {
			return &sz
		}
	}
	return nil
}
