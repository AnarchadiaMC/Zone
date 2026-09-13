package database

import (
	"context"
	"database/sql"
	"time"
	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
}

func Open(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(SchemaSQL); err != nil {
		return nil, err
	}

	return &DB{db: db}, nil
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
}

type InventoryItem struct {
	ID          int
	ItemSection string
	Condition   float32
	AmmoCurrent int
	Slot        int
}

type DBWriteJob struct {
	query string
	args  []interface{}
}

func (d *DB) RawDB() *sql.DB {
	return d.db
}

func (d *DB) AutoProvision(uuid, hwid, nick string) error {
	_, err := d.db.Exec(`INSERT OR IGNORE INTO accounts (client_uuid, hwid_hash, nickname, created_at, last_seen) VALUES (?, ?, ?, ?, ?)`, uuid, hwid, nick, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT OR IGNORE INTO characters (client_uuid, updated_at) VALUES (?, ?)`, uuid, time.Now().Unix())
	return err
}

func (d *DB) LoadCharacter(uuid string) (*Character, error) {
	row := d.db.QueryRow("SELECT level_name, pos_x, pos_y, pos_z, yaw, faction, health FROM characters WHERE client_uuid=?", uuid)
	var c Character
	c.ClientUUID = uuid
	err := row.Scan(&c.LevelName, &c.PosX, &c.PosY, &c.PosZ, &c.Yaw, &c.Faction, &c.Health)
	return &c, err
}

func (d *DB) SaveCharacter(c *Character) error {
	_, err := d.db.Exec("UPDATE characters SET level_name=?, pos_x=?, pos_y=?, pos_z=?, yaw=?, faction=?, health=?, updated_at=? WHERE client_uuid=?",
		c.LevelName, c.PosX, c.PosY, c.PosZ, c.Yaw, c.Faction, c.Health, time.Now().Unix(), c.ClientUUID)
	return err
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

func (d *DB) StartWriteQueue(ctx context.Context) chan *DBWriteJob {
	q := make(chan *DBWriteJob, 1000)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-q:
				_, _ = d.db.Exec(job.query, job.args...)
			}
		}
	}()
	return q
}
