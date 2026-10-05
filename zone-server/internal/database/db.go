package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	_ "modernc.org/sqlite"
	"strings"
	"time"
)

type DB struct {
	db *sql.DB
}

func Open(dbPath string) (*DB, error) {
	// PRODUCTION FIX: full WAL tuning per spec (busy_timeout avoids
	// "database is locked" under concurrent AutoProvision + write queue;
	// foreign_keys enforces cascades; cache_size 64MB page cache).
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=cache_size(-64000)")
	if err != nil {
		return nil, err
	}
	// Single-writer serialization: SQLite WAL reads concurrently, but writes
	// must serialize through one conn to avoid lock contention with the
	// async write-behind queue.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(SchemaSQL); err != nil {
		return nil, err
	}
	if err := migrateCharacterColumns(db); err != nil {
		return nil, err
	}

	return &DB{db: db}, nil
}

func (d *DB) Close() error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

type Character struct {
	ClientUUID string
	LevelName  string
	PosX       float32
	PosY       float32
	PosZ       float32
	Yaw        float32
	Faction    string
	Health     float32
	ProfileRev int
	Dead       int
	CreatedAt  int64
}

type InventoryItem struct {
	ID          int
	ItemSection string
	Condition   float32
	AmmoCurrent int
	Slot        int
}

type DBWriteJob struct {
	Query string
	Args  []interface{}
}

func NewDBWriteJob(query string, args ...interface{}) *DBWriteJob {
	return &DBWriteJob{Query: query, Args: args}
}

type StashRecord struct {
	StashID      uint32
	LevelName    string
	PosX         float32
	PosY         float32
	PosZ         float32
	OwnerUUID    string
	Passcode     string
	ContentsJSON string
	CreatedAt    int64
	UpdatedAt    int64
}

func (d *DB) RawDB() *sql.DB {
	return d.db
}

func (d *DB) AutoProvision(uuid, hwid, nick string) error {
	// PRODUCTION FIX: atomic single transaction (old two-Exec path could leave
	// an account row without a character row on crash).
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO accounts (client_uuid, hwid_hash, nickname, created_at, last_seen) VALUES (?, ?, ?, ?, ?)`, uuid, hwid, nick, now, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO characters (client_uuid, profile_rev, created_at, updated_at) VALUES (?, 0, ?, ?)`, uuid, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) LoadCharacter(uuid string) (*Character, error) {
	row := d.db.QueryRow("SELECT level_name, pos_x, pos_y, pos_z, yaw, faction, health, profile_rev, dead, created_at FROM characters WHERE client_uuid=?", uuid)
	var c Character
	c.ClientUUID = uuid
	err := row.Scan(&c.LevelName, &c.PosX, &c.PosY, &c.PosZ, &c.Yaw, &c.Faction, &c.Health, &c.ProfileRev, &c.Dead, &c.CreatedAt)
	return &c, err
}

func (d *DB) SaveCharacter(c *Character) error {
	_, err := d.db.Exec("UPDATE characters SET level_name=?, pos_x=?, pos_y=?, pos_z=?, yaw=?, faction=?, health=?, profile_rev=?, dead=?, updated_at=? WHERE client_uuid=?",
		c.LevelName, c.PosX, c.PosY, c.PosZ, c.Yaw, c.Faction, c.Health, c.ProfileRev, c.Dead, time.Now().Unix(), c.ClientUUID)
	return err
}

var AllowedFactions = map[string]struct{}{
	"stalker": {}, "dolg": {}, "freedom": {}, "csky": {},
	"ecolog": {}, "killer": {}, "army": {}, "bandit": {},
	"monolith": {}, "renegade": {}, "greh": {}, "isg": {},
	"zombied": {},
}

var ValidateNewCharacter = func(faction, loadout string) error {
	if _, ok := AllowedFactions[faction]; !ok {
		return fmt.Errorf("invalid faction %q", faction)
	}
	if len(loadout) > 4096 {
		return fmt.Errorf("loadout too long")
	}
	if loadout == "" {
		return nil
	}
	count := 0
	for _, e := range strings.Split(loadout, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		count++
		if count > 256 {
			return fmt.Errorf("too many loadout entries")
		}
		sec := e
		if i := strings.Index(e, ":"); i >= 0 {
			sec = e[:i]
		}
		if len(sec) == 0 || len(sec) > 64 {
			return fmt.Errorf("invalid loadout section %q", sec)
		}
		for _, r := range sec {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
				continue
			}
			return fmt.Errorf("invalid loadout section %q", sec)
		}
	}
	return nil
}

func migrateCharacterColumns(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(characters)")
	if err != nil {
		return err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	adds := map[string]string{
		"profile_rev": "ALTER TABLE characters ADD COLUMN profile_rev INTEGER NOT NULL DEFAULT 1",
		"dead":        "ALTER TABLE characters ADD COLUMN dead INTEGER NOT NULL DEFAULT 0",
		"created_at":  "ALTER TABLE characters ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0",
	}
	for col, stmt := range adds {
		if !have[col] {
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	_, _ = db.Exec("UPDATE characters SET created_at=updated_at WHERE created_at=0")
	// PRODUCTION FIX: promote only characters with real profile provenance.
	// AutoProvision placeholders insert created_at == updated_at and have no
	// inventory; the old created_at!=0 predicate promoted every half-created
	// account on restart, making the client skip character creation.
	_, _ = db.Exec(`UPDATE characters SET profile_rev=1
		WHERE profile_rev=0 AND created_at!=0
		  AND (updated_at>created_at
		       OR EXISTS (SELECT 1 FROM character_inventory ci WHERE ci.client_uuid=characters.client_uuid))`)
	return nil
}

func (d *DB) HasCharacter(uuid string) (bool, error) {
	var one int
	err := d.db.QueryRow("SELECT 1 FROM characters WHERE client_uuid=?", uuid).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (d *DB) CreateCharacter(uuid, faction, loadout string) error {
	if err := ValidateNewCharacter(faction, loadout); err != nil {
		return err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	var one int
	err = tx.QueryRow("SELECT 1 FROM characters WHERE client_uuid=?", uuid).Scan(&one)
	if err == nil {
		if _, err := tx.Exec(`UPDATE characters SET faction=?, level_name='l01_escape', pos_x=-211.3, pos_y=-20.2, pos_z=-145.8, yaw=0, health=1.0, profile_rev=1, dead=0, updated_at=? WHERE client_uuid=?`, faction, now, uuid); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM character_inventory WHERE client_uuid=?`, uuid); err != nil {
			return err
		}
	} else if err == sql.ErrNoRows {
		if _, err := tx.Exec(`INSERT INTO characters (client_uuid, faction, profile_rev, dead, created_at, updated_at) VALUES (?, ?, 1, 0, ?, ?)`, uuid, faction, now, now); err != nil {
			return err
		}
	} else {
		return err
	}
	if loadout != "" {
		for _, e := range strings.Split(loadout, ",") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			sec := e
			ammo := 0
			if i := strings.Index(e, ":"); i >= 0 {
				sec = strings.TrimSpace(e[:i])
				_, _ = fmt.Sscanf(strings.TrimSpace(e[i+1:]), "%d", &ammo)
			}
			if sec == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO character_inventory (client_uuid, item_section, ammo_current) VALUES (?, ?, ?)`, uuid, sec, ammo); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (d *DB) FlushPlayerTransform(uuid string, x, y, z, yaw float32, health float32) error {
	_, err := d.db.Exec("UPDATE characters SET pos_x=?, pos_y=?, pos_z=?, yaw=?, health=?, updated_at=? WHERE client_uuid=?", x, y, z, yaw, health, time.Now().Unix(), uuid)
	return err
}

func (d *DB) GetCharacterInventory(uuid string) ([]InventoryItem, error) {
	rows, err := d.db.Query("SELECT id, item_section, condition, ammo_current, slot FROM character_inventory WHERE client_uuid=?", uuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []InventoryItem
	for rows.Next() {
		var i InventoryItem
		if err := rows.Scan(&i.ID, &i.ItemSection, &i.Condition, &i.AmmoCurrent, &i.Slot); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (d *DB) IsPlayerBanned(uuid string) (bool, string, error) {
	row := d.db.QueryRow("SELECT banned, ban_reason FROM accounts WHERE client_uuid=?", uuid)
	var banned int
	var reason sql.NullString
	if err := row.Scan(&banned, &reason); err != nil {
		return false, "", err
	}
	return banned > 0, reason.String, nil
}

func (d *DB) BanAccount(uuid string, reason string) error {
	_, err := d.db.Exec("UPDATE accounts SET banned = 1, ban_reason = ? WHERE client_uuid = ?", reason, uuid)
	return err
}

func (d *DB) GetStash(stashID uint32) (*StashRecord, error) {
	row := d.db.QueryRow("SELECT stash_id, level_name, pos_x, pos_y, pos_z, COALESCE(owner_uuid, ''), COALESCE(passcode, ''), contents_json, created_at, updated_at FROM world_stashes WHERE stash_id = ?", stashID)
	var s StashRecord
	if err := row.Scan(&s.StashID, &s.LevelName, &s.PosX, &s.PosY, &s.PosZ, &s.OwnerUUID, &s.Passcode, &s.ContentsJSON, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *DB) SaveStash(stashID uint32, level string, x, y, z float32, contentsJSON string) error {
	now := time.Now().Unix()
	if contentsJSON == "" {
		contentsJSON = "[]"
	}
	if stashID == 0 {
		_, err := d.db.Exec(`INSERT INTO world_stashes (level_name, pos_x, pos_y, pos_z, contents_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			level, x, y, z, contentsJSON, now, now)
		return err
	}
	_, err := d.db.Exec(`INSERT INTO world_stashes (stash_id, level_name, pos_x, pos_y, pos_z, contents_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(stash_id) DO UPDATE SET
			level_name=excluded.level_name,
			pos_x=excluded.pos_x,
			pos_y=excluded.pos_y,
			pos_z=excluded.pos_z,
			contents_json=excluded.contents_json,
			updated_at=excluded.updated_at`,
		stashID, level, x, y, z, contentsJSON, now, now)
	return err
}

func (d *DB) UpdateStashContents(stashID uint32, contentsJSON string) error {
	if contentsJSON == "" {
		contentsJSON = "[]"
	}
	res, err := d.db.Exec("UPDATE world_stashes SET contents_json = ?, updated_at = ? WHERE stash_id = ?", contentsJSON, time.Now().Unix(), stashID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *DB) StartWriteQueue(ctx context.Context) chan *DBWriteJob {
	q := make(chan *DBWriteJob, 1000)
	go func() {
		for {
			select {
			case <-ctx.Done():
				for len(q) > 0 {
					job := <-q
					if job != nil {
						if _, err := d.db.Exec(job.Query, job.Args...); err != nil {
							log.Printf("database write error during shutdown drain: %v (query: %s)", err, job.Query)
						}
					}
				}
				return
			case job := <-q:
				if job != nil {
					if _, err := d.db.Exec(job.Query, job.Args...); err != nil {
						log.Printf("database write error: %v (query: %s)", err, job.Query)
					}
				}
			}
		}
	}()
	return q
}
