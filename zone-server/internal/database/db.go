package database

import (
	"context"
	"database/sql"
	"errors"
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
	if err := migrateInventoryColumns(db); err != nil {
		return nil, err
	}
	if err := migrateAISquadColumns(db); err != nil {
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
	ItemCount   int
	Condition   float32
	AmmoCurrent int
	Slot        int
}

// WorldItem is one server-authoritative item stack lying in the world.
type WorldItem struct {
	ID        int64
	LevelName string
	PosX      float32
	PosY      float32
	PosZ      float32
	Section   string
	Count     int
	Condition float32
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

// Sentinel errors returned by ValidateNewCharacter. Callers use errors.Is to
// map a failure onto a protocol error code.
var (
	ErrInvalidFaction = errors.New("invalid faction")
	ErrInvalidLoadout = errors.New("invalid loadout")
)

var ValidateNewCharacter = func(faction, loadout string) error {
	if _, ok := AllowedFactions[faction]; !ok {
		return fmt.Errorf("%w %q", ErrInvalidFaction, faction)
	}
	if len(loadout) > 4096 {
		return fmt.Errorf("%w: too long", ErrInvalidLoadout)
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
			return fmt.Errorf("%w: too many entries", ErrInvalidLoadout)
		}
		sec := e
		if i := strings.Index(e, ":"); i >= 0 {
			sec = e[:i]
		}
		if len(sec) == 0 || len(sec) > 64 {
			return fmt.Errorf("%w: invalid section %q", ErrInvalidLoadout, sec)
		}
		for _, r := range sec {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
				continue
			}
			return fmt.Errorf("%w: invalid section %q", ErrInvalidLoadout, sec)
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

// migrateInventoryColumns adds the stacking count column to databases created
// before the world-item ledger existed. Fresh databases get it from SchemaSQL.
func migrateInventoryColumns(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(character_inventory)")
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
	if !have["item_count"] {
		if _, err := db.Exec("ALTER TABLE character_inventory ADD COLUMN item_count INTEGER NOT NULL DEFAULT 1"); err != nil {
			return err
		}
	}
	return nil
}

// migrateAISquadColumns adds the AI replication metadata columns (label,
// patrol_radius, walk_speed, run_speed) to databases created before the AI
// puppet wave. Fresh databases get them from SchemaSQL.
func migrateAISquadColumns(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(ai_squads)")
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
		"label":         "ALTER TABLE ai_squads ADD COLUMN label TEXT NOT NULL DEFAULT ''",
		"patrol_radius": "ALTER TABLE ai_squads ADD COLUMN patrol_radius REAL NOT NULL DEFAULT 32.0",
		"walk_speed":    "ALTER TABLE ai_squads ADD COLUMN walk_speed REAL NOT NULL DEFAULT 1.5",
		"run_speed":     "ALTER TABLE ai_squads ADD COLUMN run_speed REAL NOT NULL DEFAULT 3.0",
	}
	for col, stmt := range adds {
		if !have[col] {
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
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

func (d *DB) CreateCharacter(uuid, faction, loadout string, money uint32) error {
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
	// Starter money: only overwrite the schema default when the client picked a
	// positive amount (a zero/omitted value keeps the 5000 ruble default).
	if money > 0 {
		if _, err := tx.Exec(`UPDATE characters SET rubles=?, updated_at=? WHERE client_uuid=?`, int64(money), now, uuid); err != nil {
			return err
		}
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
	rows, err := d.db.Query("SELECT id, item_section, item_count, condition, ammo_current, slot FROM character_inventory WHERE client_uuid=?", uuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []InventoryItem
	for rows.Next() {
		var i InventoryItem
		if err := rows.Scan(&i.ID, &i.ItemSection, &i.ItemCount, &i.Condition, &i.AmmoCurrent, &i.Slot); err != nil {
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

// Ledger sentinel errors. Callers map these onto OpItemUpdate results.
var (
	ErrInsufficientItems = errors.New("insufficient items")
	ErrWorldItemNotFound = errors.New("world item not found")
)

// AvailableItemCount returns how many copies of section the character holds
// across all inventory rows, used to build correction payloads.
func (d *DB) AvailableItemCount(uuid, section string) (int, error) {
	var total sql.NullInt64
	err := d.db.QueryRow("SELECT COALESCE(SUM(item_count), 0) FROM character_inventory WHERE client_uuid=? AND item_section=?", uuid, section).Scan(&total)
	if err != nil {
		return 0, err
	}
	return int(total.Int64), nil
}

// DropItemToWorld atomically removes count copies of section from the
// character's inventory and inserts one world_items row. It returns the new
// world item id and the number of copies left in the inventory. Insufficient
// stock returns ErrInsufficientItems (with the total held as the second value)
// and leaves the inventory untouched.
func (d *DB) DropItemToWorld(uuid, level, section string, count int, x, y, z, condition float32) (int64, int, error) {
	if count <= 0 {
		return 0, 0, ErrInsufficientItems
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	total, err := removeInventoryItemsLocked(tx, uuid, section, count)
	if err != nil {
		if errors.Is(err, ErrInsufficientItems) {
			return 0, total, ErrInsufficientItems
		}
		return 0, 0, err
	}

	res, err := tx.Exec(`INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section, item_count, condition) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		level, x, y, z, section, count, condition)
	if err != nil {
		return 0, 0, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return newID, total - count, nil
}

// ConsumeItem atomically removes count copies of section from the character's
// inventory inside one transaction (OpItemAction action=3). It returns the
// remaining inventory count. Insufficient stock returns ErrInsufficientItems
// (with the total held as the first value) and removes nothing.
func (d *DB) ConsumeItem(uuid, section string, count int) (int, error) {
	if count <= 0 {
		return 0, ErrInsufficientItems
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	total, err := removeInventoryItemsLocked(tx, uuid, section, count)
	if err != nil {
		return total, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total - count, nil
}

// removeInventoryItemsLocked removes count copies of section from
// character_inventory inside tx, consuming rows in id order. It returns the
// total held before removal. ErrInsufficientItems means nothing was removed.
func removeInventoryItemsLocked(tx *sql.Tx, uuid, section string, count int) (int, error) {
	rows, err := tx.Query("SELECT id, item_count FROM character_inventory WHERE client_uuid=? AND item_section=? ORDER BY id", uuid, section)
	if err != nil {
		return 0, err
	}
	type invRow struct {
		id    int
		count int
	}
	var held []invRow
	total := 0
	for rows.Next() {
		var r invRow
		if err := rows.Scan(&r.id, &r.count); err != nil {
			rows.Close()
			return 0, err
		}
		held = append(held, r)
		total += r.count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if total < count {
		return total, ErrInsufficientItems
	}

	remaining := count
	for _, r := range held {
		if remaining <= 0 {
			break
		}
		take := r.count
		if take > remaining {
			take = remaining
		}
		if take == r.count {
			if _, err := tx.Exec("DELETE FROM character_inventory WHERE id=?", r.id); err != nil {
				return 0, err
			}
		} else {
			if _, err := tx.Exec("UPDATE character_inventory SET item_count = item_count - ? WHERE id=?", take, r.id); err != nil {
				return 0, err
			}
		}
		remaining -= take
	}
	return total, nil
}

// GetWorldItem loads one world item row.
func (d *DB) GetWorldItem(itemID int64) (*WorldItem, error) {
	row := d.db.QueryRow("SELECT id, level_name, pos_x, pos_y, pos_z, section, item_count, condition FROM world_items WHERE id=?", itemID)
	var wi WorldItem
	err := row.Scan(&wi.ID, &wi.LevelName, &wi.PosX, &wi.PosY, &wi.PosZ, &wi.Section, &wi.Count, &wi.Condition)
	if err == sql.ErrNoRows {
		return nil, ErrWorldItemNotFound
	}
	if err != nil {
		return nil, err
	}
	return &wi, nil
}

// PickupWorldItem atomically deletes a world item and credits its server-side
// stack (section and count come from the DB row, never from the client) into
// character_inventory. A concurrent second pickup sees ErrWorldItemNotFound
// because the DELETE reports zero affected rows; the first transaction wins.
func (d *DB) PickupWorldItem(uuid string, itemID int64) (*WorldItem, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var wi WorldItem
	err = tx.QueryRow("SELECT id, level_name, pos_x, pos_y, pos_z, section, item_count, condition FROM world_items WHERE id=?", itemID).
		Scan(&wi.ID, &wi.LevelName, &wi.PosX, &wi.PosY, &wi.PosZ, &wi.Section, &wi.Count, &wi.Condition)
	if err == sql.ErrNoRows {
		return nil, ErrWorldItemNotFound
	}
	if err != nil {
		return nil, err
	}

	res, err := tx.Exec("DELETE FROM world_items WHERE id=?", itemID)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrWorldItemNotFound
	}

	var invID int
	err = tx.QueryRow("SELECT id FROM character_inventory WHERE client_uuid=? AND item_section=? ORDER BY id LIMIT 1", uuid, wi.Section).Scan(&invID)
	switch {
	case err == sql.ErrNoRows:
		if _, err := tx.Exec("INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)",
			uuid, wi.Section, wi.Count, wi.Condition); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if _, err := tx.Exec("UPDATE character_inventory SET item_count = item_count + ? WHERE id=?", wi.Count, invID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &wi, nil
}

// InsertAudit appends one audit_log row for security-relevant events
// (item ledger actions, lag-switch kicks).
func (d *DB) InsertAudit(clientUUID, eventType, detail string) error {
	_, err := d.db.Exec("INSERT INTO audit_log (timestamp, client_uuid, event_type, detail) VALUES (?, ?, ?, ?)",
		time.Now().Unix(), clientUUID, eventType, detail)
	return err
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
