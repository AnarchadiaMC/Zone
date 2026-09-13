package game

import (
	"database/sql"
	"errors"
	"sync"
	"time"

	"zone-online/zone-server/internal/database"
)

var (
	ErrCharacterNotFound = errors.New("character not found")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrItemNotFound      = errors.New("item not found in inventory")
	ErrDBNotConfigured   = errors.New("database not configured")
)

type EconomyManager struct {
	db *database.DB
	mu sync.RWMutex
}

func NewEconomyManager(dbs ...*database.DB) *EconomyManager {
	mgr := &EconomyManager{}
	if len(dbs) > 0 {
		mgr.db = dbs[0]
	}
	return mgr
}

func (e *EconomyManager) SetDB(db *database.DB) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.db = db
}

func (e *EconomyManager) getDB() (*database.DB, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.db == nil {
		return nil, ErrDBNotConfigured
	}
	return e.db, nil
}

// GetBalance returns current rubles for a character.
func (e *EconomyManager) GetBalance(uuid string) (int, error) {
	db, err := e.getDB()
	if err != nil {
		return 0, err
	}

	var rubles int
	row := db.RawDB().QueryRow("SELECT rubles FROM characters WHERE client_uuid = ?", uuid)
	if err := row.Scan(&rubles); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrCharacterNotFound
		}
		return 0, err
	}
	return rubles, nil
}

// AddCurrency adjusts player's rubles. If amount is negative, checks that resulting balance is not negative.
func (e *EconomyManager) AddCurrency(uuid string, amount int) error {
	db, err := e.getDB()
	if err != nil {
		return err
	}

	tx, err := db.RawDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var rubles int
	row := tx.QueryRow("SELECT rubles FROM characters WHERE client_uuid = ?", uuid)
	if err := row.Scan(&rubles); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCharacterNotFound
		}
		return err
	}

	newBalance := rubles + amount
	if newBalance < 0 {
		return ErrInsufficientFunds
	}

	_, err = tx.Exec("UPDATE characters SET rubles = ?, updated_at = ? WHERE client_uuid = ?", newBalance, time.Now().Unix(), uuid)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// BuyItem checks if player has enough rubles. If so, deducts price, inserts item into character_inventory, and returns true.
// If player cannot afford, returns false, nil.
func (e *EconomyManager) BuyItem(uuid string, itemSection string, price int) (bool, error) {
	if price < 0 {
		return false, errors.New("price cannot be negative")
	}

	db, err := e.getDB()
	if err != nil {
		return false, err
	}

	tx, err := db.RawDB().Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var rubles int
	row := tx.QueryRow("SELECT rubles FROM characters WHERE client_uuid = ?", uuid)
	if err := row.Scan(&rubles); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrCharacterNotFound
		}
		return false, err
	}

	if rubles < price {
		return false, nil
	}

	newBalance := rubles - price
	now := time.Now().Unix()
	if _, err := tx.Exec("UPDATE characters SET rubles = ?, updated_at = ? WHERE client_uuid = ?", newBalance, now, uuid); err != nil {
		return false, err
	}

	if _, err := tx.Exec("INSERT INTO character_inventory (client_uuid, item_section, condition, ammo_current, slot) VALUES (?, ?, 1.0, 0, -1)", uuid, itemSection); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}

	return true, nil
}

// SellItem checks if player owns the item in character_inventory. If so, removes one instance of the item, adds price to rubles, and returns true.
// If player does not own the item, returns false, nil.
func (e *EconomyManager) SellItem(uuid string, itemSection string, price int) (bool, error) {
	if price < 0 {
		return false, errors.New("price cannot be negative")
	}

	db, err := e.getDB()
	if err != nil {
		return false, err
	}

	tx, err := db.RawDB().Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var itemID int
	row := tx.QueryRow("SELECT id FROM character_inventory WHERE client_uuid = ? AND item_section = ? LIMIT 1", uuid, itemSection)
	if err := row.Scan(&itemID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	if _, err := tx.Exec("DELETE FROM character_inventory WHERE id = ?", itemID); err != nil {
		return false, err
	}

	now := time.Now().Unix()
	if _, err := tx.Exec("UPDATE characters SET rubles = rubles + ?, updated_at = ? WHERE client_uuid = ?", price, now, uuid); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}

	return true, nil
}

// GetTier returns economy_tier for character.
func (e *EconomyManager) GetTier(uuid string) (int, error) {
	db, err := e.getDB()
	if err != nil {
		return 0, err
	}

	var tier int
	row := db.RawDB().QueryRow("SELECT economy_tier FROM characters WHERE client_uuid = ?", uuid)
	if err := row.Scan(&tier); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrCharacterNotFound
		}
		return 0, err
	}
	return tier, nil
}

// SetTier updates economy_tier for character.
func (e *EconomyManager) SetTier(uuid string, tier int) error {
	db, err := e.getDB()
	if err != nil {
		return err
	}

	res, err := db.RawDB().Exec("UPDATE characters SET economy_tier = ?, updated_at = ? WHERE client_uuid = ?", tier, time.Now().Unix(), uuid)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrCharacterNotFound
	}
	return nil
}
