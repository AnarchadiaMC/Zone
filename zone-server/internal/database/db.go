package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	_ "modernc.org/sqlite"
	"strings"
	"sync"
	"time"
)

type DB struct {
	db *sql.DB
	// writeWG tracks the write-behind worker started by StartWriteQueue so a
	// shutdown can drain and join it before Close.
	writeWG sync.WaitGroup
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
	if err := runMigrations(db); err != nil {
		return nil, err
	}

	return &DB{db: db}, nil
}

// Schema migration versions. Each boot migration is applied at most once and
// stamped in schema_version; the ALTER guards inside each step stay idempotent
// so a pre-versioning database (version 0 with some columns already added) is
// upgraded safely.
const (
	schemaVersionCharacterColumns = 1
	schemaVersionInventoryColumns = 2
	schemaVersionAISquadColumns   = 3
	schemaVersionLatest           = schemaVersionAISquadColumns
)

func schemaVersion(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow("SELECT version FROM schema_version WHERE id=1").Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return v, nil
}

func setSchemaVersion(db *sql.DB, version int) error {
	_, err := db.Exec(`INSERT INTO schema_version (id, version) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET version=excluded.version`, version)
	return err
}

// runMigrations applies each versioned migration whose version is newer than
// the stamped schema_version, then records it. Data-mutating backfills (the
// character profile_rev promotion) therefore run once per database instead of
// on every Open.
func runMigrations(db *sql.DB) error {
	version, err := schemaVersion(db)
	if err != nil {
		return err
	}
	if version >= schemaVersionLatest {
		return nil
	}
	if version < schemaVersionCharacterColumns {
		if err := migrateCharacterColumns(db); err != nil {
			return err
		}
		if err := setSchemaVersion(db, schemaVersionCharacterColumns); err != nil {
			return err
		}
		version = schemaVersionCharacterColumns
	}
	if version < schemaVersionInventoryColumns {
		if err := migrateInventoryColumns(db); err != nil {
			return err
		}
		if err := setSchemaVersion(db, schemaVersionInventoryColumns); err != nil {
			return err
		}
		version = schemaVersionInventoryColumns
	}
	if version < schemaVersionAISquadColumns {
		if err := migrateAISquadColumns(db); err != nil {
			return err
		}
		if err := setSchemaVersion(db, schemaVersionAISquadColumns); err != nil {
			return err
		}
	}
	return nil
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
	return scanStash(row)
}

// scanStash decodes one world_stashes row selected with the canonical column
// order used by GetStash/FindStashAt.
func scanStash(row *sql.Row) (*StashRecord, error) {
	var s StashRecord
	if err := row.Scan(&s.StashID, &s.LevelName, &s.PosX, &s.PosY, &s.PosZ, &s.OwnerUUID, &s.Passcode, &s.ContentsJSON, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// FindStashAt returns the stash at an exact (level, position) key or
// sql.ErrNoRows. Positions are stored rounded onto the 0.5 m key grid, so the
// exact equality match is deliberate; callers round before calling.
func (d *DB) FindStashAt(level string, x, y, z float32) (*StashRecord, error) {
	row := d.db.QueryRow(`SELECT stash_id, level_name, pos_x, pos_y, pos_z, COALESCE(owner_uuid, ''), COALESCE(passcode, ''), contents_json, created_at, updated_at
		FROM world_stashes WHERE level_name = ? AND pos_x = ? AND pos_y = ? AND pos_z = ?
		ORDER BY stash_id LIMIT 1`, level, x, y, z)
	return scanStash(row)
}

// FindOrCreateStashAt resolves the ownerless world stash at an exact (level,
// position) key, inserting an empty one when absent. SELECT and INSERT share
// one transaction; the caller still serializes concurrent creates (see
// StashManager.FindOrCreateWorldStash) so one key never materialises two
// ownerless stashes. owner_uuid/passcode stay NULL, i.e. a world stash any
// nearby character may use.
func (d *DB) FindOrCreateStashAt(level string, x, y, z float32) (*StashRecord, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	selectRow := tx.QueryRow(`SELECT stash_id, level_name, pos_x, pos_y, pos_z, COALESCE(owner_uuid, ''), COALESCE(passcode, ''), contents_json, created_at, updated_at
		FROM world_stashes WHERE level_name = ? AND pos_x = ? AND pos_y = ? AND pos_z = ?
		ORDER BY stash_id LIMIT 1`, level, x, y, z)
	rec, err := scanStash(selectRow)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return rec, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	now := time.Now().Unix()
	res, err := tx.Exec(`INSERT INTO world_stashes (level_name, pos_x, pos_y, pos_z, owner_uuid, passcode, contents_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULL, NULL, '[]', ?, ?)`, level, x, y, z, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &StashRecord{
		StashID:      uint32(id),
		LevelName:    level,
		PosX:         x,
		PosY:         y,
		PosZ:         z,
		ContentsJSON: "[]",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
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

// ErrWorldItemLimit is returned when a drop would exceed the configured
// per-level world item cap. Nothing is removed or inserted.
var ErrWorldItemLimit = errors.New("world item limit reached")

// DropItemToWorld atomically removes count copies of section from the
// character's inventory and inserts one world_items row. The world item's
// condition is derived from the removed inventory row, never from a
// client-supplied value, so a low-condition stack cannot be laundered into a
// high-condition world item. preferredBucket (0-100, or -1 for any) selects
// which inventory condition bucket is consumed first when several stacks of
// the section exist. maxPerLevel > 0 rejects the drop with ErrWorldItemLimit
// when the level already holds that many world item rows.
// It returns the new world item id, the number of copies left in the
// inventory and the condition (0..1) of the row actually consumed.
// Insufficient stock returns ErrInsufficientItems (with the total held as the
// second value) and leaves the inventory untouched.
func (d *DB) DropItemToWorld(uuid, level, section string, count int, x, y, z float32, preferredBucket, maxPerLevel int) (int64, int, float32, error) {
	if count <= 0 {
		return 0, 0, 0, ErrInsufficientItems
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()

	if maxPerLevel > 0 {
		var held int
		if err := tx.QueryRow("SELECT COUNT(*) FROM world_items WHERE level_name=?", level).Scan(&held); err != nil {
			return 0, 0, 0, err
		}
		if held >= maxPerLevel {
			return 0, 0, 0, ErrWorldItemLimit
		}
	}

	total, condition, err := RemoveInventoryItemsLocked(tx, uuid, section, count, preferredBucket)
	if err != nil {
		if errors.Is(err, ErrInsufficientItems) {
			return 0, total, 0, ErrInsufficientItems
		}
		return 0, 0, 0, err
	}

	res, err := tx.Exec(`INSERT INTO world_items (level_name, pos_x, pos_y, pos_z, section, item_count, condition) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		level, x, y, z, section, count, clampCondition01(condition))
	if err != nil {
		return 0, 0, 0, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return newID, total - count, condition, nil
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

	total, _, err := RemoveInventoryItemsLocked(tx, uuid, section, count, -1)
	if err != nil {
		return total, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return total - count, nil
}

// RemoveInventoryItemsLocked removes count copies of section from
// character_inventory inside tx, consuming rows in preferred-condition then id
// order. A row is decremented, never deleted while copies remain, so storing or
// dropping 1 copy of a 30-stack leaves 29 behind. preferredBucket (0-100, or
// -1 for no preference) consumes rows whose 0-100 condition bucket matches
// first. It returns the total held before removal and the condition (0..1) of
// the first row actually consumed; ErrInsufficientItems means nothing was
// removed.
func RemoveInventoryItemsLocked(tx *sql.Tx, uuid, section string, count int, preferredBucket int) (int, float32, error) {
	rows, err := tx.Query(`SELECT id, item_count, condition FROM character_inventory WHERE client_uuid=? AND item_section=?
		ORDER BY CASE WHEN ? >= 0 AND CAST(ROUND(condition*100) AS INTEGER) = ? THEN 0 ELSE 1 END, id`,
		uuid, section, preferredBucket, preferredBucket)
	if err != nil {
		return 0, 0, err
	}
	type invRow struct {
		id        int
		count     int
		condition float32
	}
	var held []invRow
	total := 0
	for rows.Next() {
		var r invRow
		if err := rows.Scan(&r.id, &r.count, &r.condition); err != nil {
			rows.Close()
			return 0, 0, err
		}
		held = append(held, r)
		total += r.count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()
	if total < count {
		return total, 0, ErrInsufficientItems
	}

	remaining := count
	removedCondition := float32(0)
	consumedAny := false
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
				return 0, 0, err
			}
		} else {
			if _, err := tx.Exec("UPDATE character_inventory SET item_count = item_count - ? WHERE id=?", take, r.id); err != nil {
				return 0, 0, err
			}
		}
		if !consumedAny {
			removedCondition = r.condition
			consumedAny = true
		}
		remaining -= take
	}
	return total, removedCondition, nil
}

// CreditInventoryLocked adds count copies of section to character_inventory
// inside tx, merging by section + 0-100 condition bucket so different
// conditions never repair each other.
//
// Ammo limitation: the wire protocol has no ammo field, so a credit for a
// section whose existing inventory row carries ammo semantics (a single-object
// row with ammo_current > 0, e.g. a weapon's loaded rounds) is added to
// ammo_current instead of materialising count zero-ammo item copies. Sections
// with no such row are credited as plain stack copies; this is the documented
// residual ambiguity because the database holds no per-section type table.
func CreditInventoryLocked(tx *sql.Tx, uuid, section string, count int, conditionBucket uint8) error {
	var id, itemCount, ammo int
	err := tx.QueryRow(`SELECT id, item_count, ammo_current FROM character_inventory
		WHERE client_uuid=? AND item_section=? AND CAST(ROUND(condition*100) AS INTEGER)=?
		ORDER BY id LIMIT 1`, uuid, section, int(conditionBucket)).Scan(&id, &itemCount, &ammo)
	switch {
	case err == nil:
		if ammo > 0 && itemCount <= 1 {
			_, err = tx.Exec(`UPDATE character_inventory SET ammo_current = MIN(ammo_current + ?, 65535) WHERE id=?`, count, id)
			return err
		}
		_, err = tx.Exec(`UPDATE character_inventory SET item_count = item_count + ? WHERE id=?`, count, id)
		return err
	case err != sql.ErrNoRows:
		return err
	}

	// No same-bucket row: prefer an existing ammo-bearing single-object row so
	// rounds are not split into zero-ammo copies across conditions.
	err = tx.QueryRow(`SELECT id FROM character_inventory
		WHERE client_uuid=? AND item_section=? AND ammo_current > 0 AND item_count <= 1
		ORDER BY id LIMIT 1`, uuid, section).Scan(&id)
	switch {
	case err == nil:
		if _, err := tx.Exec(`UPDATE character_inventory SET ammo_current = MIN(ammo_current + ?, 65535) WHERE id=?`, count, id); err != nil {
			return err
		}
		return nil
	case err != sql.ErrNoRows:
		return err
	}

	cond := float64(conditionBucket) / 100.0
	_, err = tx.Exec(`INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)`,
		uuid, section, count, cond)
	return err
}

// GetWorldItemsByLevel loads every world item row on a level, newest first.
func (d *DB) GetWorldItemsByLevel(level string) ([]WorldItem, error) {
	rows, err := d.db.Query("SELECT id, level_name, pos_x, pos_y, pos_z, section, item_count, condition FROM world_items WHERE level_name=? ORDER BY id", level)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WorldItem
	for rows.Next() {
		var wi WorldItem
		if err := rows.Scan(&wi.ID, &wi.LevelName, &wi.PosX, &wi.PosY, &wi.PosZ, &wi.Section, &wi.Count, &wi.Condition); err != nil {
			return nil, err
		}
		items = append(items, wi)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// DeleteWorldItemsOlderThan removes every world_items row created at or before
// cutoff and returns the deleted rows so the caller can broadcast removals.
// created_at is stored by SQLite as a UTC "YYYY-MM-DD HH:MM:SS" string, which
// compares correctly as text.
func (d *DB) DeleteWorldItemsOlderThan(cutoff time.Time) ([]WorldItem, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stamp := cutoff.UTC().Format("2006-01-02 15:04:05")
	rows, err := tx.Query("SELECT id, level_name, pos_x, pos_y, pos_z, section, item_count, condition FROM world_items WHERE created_at <= ? ORDER BY id", stamp)
	if err != nil {
		return nil, err
	}
	var expired []WorldItem
	for rows.Next() {
		var wi WorldItem
		if err := rows.Scan(&wi.ID, &wi.LevelName, &wi.PosX, &wi.PosY, &wi.PosZ, &wi.Section, &wi.Count, &wi.Condition); err != nil {
			rows.Close()
			return nil, err
		}
		expired = append(expired, wi)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(expired) == 0 {
		return nil, tx.Commit()
	}
	for _, wi := range expired {
		if _, err := tx.Exec("DELETE FROM world_items WHERE id=?", wi.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return expired, nil
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

	// Credit through the shared helper: merges by section + condition bucket
	// and routes single-object ammo-bearing rows to ammo_current.
	if err := CreditInventoryLocked(tx, uuid, wi.Section, wi.Count, conditionBucket(wi.Condition)); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &wi, nil
}

// conditionBucket converts a DB REAL 0..1 condition into the 0-100 wire
// bucket used by inventory/stash merges.
func conditionBucket(condition float32) uint8 {
	return uint8(math.Round(float64(clampCondition01(condition)) * 100))
}

// clampCondition01 maps NaN/out-of-range conditions onto [0,1].
func clampCondition01(condition float32) float32 {
	c := float64(condition)
	if math.IsNaN(c) || c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return condition
}

// InsertAudit appends one audit_log row for security-relevant events
// (item ledger actions, rejected/applied damage).
func (d *DB) InsertAudit(clientUUID, eventType, detail string) error {
	_, err := d.db.Exec("INSERT INTO audit_log (timestamp, client_uuid, event_type, detail) VALUES (?, ?, ?, ?)",
		time.Now().Unix(), clientUUID, eventType, detail)
	return err
}

// StartWriteQueue starts the single write-behind worker and returns the job
// channel. The caller must stop all producers, close the channel, and call
// WaitWriteQueue before Close, so the worker drains every buffered write
// before the database handle is closed.
func (d *DB) StartWriteQueue() chan *DBWriteJob {
	q := make(chan *DBWriteJob, 1000)
	d.writeWG.Add(1)
	go func() {
		defer d.writeWG.Done()
		for job := range q {
			if job != nil {
				if _, err := d.db.Exec(job.Query, job.Args...); err != nil {
					log.Printf("database write error: %v (query: %s)", err, job.Query)
				}
			}
		}
	}()
	return q
}

// WaitWriteQueue blocks until the write-behind worker has drained the closed
// job channel and exited. It is a no-op when StartWriteQueue was never called.
func (d *DB) WaitWriteQueue() {
	d.writeWG.Wait()
}
