package game

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"

	"zone-online/zone-server/internal/database"
)

var (
	ErrStashNotFound         = errors.New("stash not found")
	ErrItemNotFoundStash     = errors.New("item not found in stash")
	ErrInsufficientCount     = errors.New("insufficient item count in stash")
	ErrStashAccessDenied     = errors.New("stash access denied")
	ErrStashWrongLevel       = errors.New("player on wrong level for stash")
	ErrStashTooFar           = errors.New("player too far from stash")
	ErrStashWrongPasscode    = errors.New("invalid stash passcode")
	ErrInvalidStashSection   = errors.New("invalid stash item section")
	ErrInvalidStashCount     = errors.New("invalid stash count")
	ErrInvalidItemCondition  = errors.New("invalid item condition")
	ErrStashContentsTooLarge = errors.New("stash contents too large")
)

// MaxStashInteractDistance is the maximum 3D distance (meters) allowed
// between a player and a stash for any interaction.
const MaxStashInteractDistance = 5.0

type StashData struct {
	StashID   uint32  `json:"stash_id"`
	LevelName string  `json:"level_name"`
	PosX      float32 `json:"pos_x"`
	PosY      float32 `json:"pos_y"`
	PosZ      float32 `json:"pos_z"`
	OwnerUUID string  `json:"owner_uuid"`
	Passcode  string  `json:"passcode"`
	Contents  []byte  `json:"contents"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// StashItem is one entry of a stash contents_json array. Condition is the
// 0-100 wire bucket used by OpContainerAction deposit merges; legacy rows
// without the field decode to bucket 0 and the legacy stash APIs keep their
// section-only matching semantics.
type StashItem struct {
	Section   string `json:"section"`
	Count     int    `json:"count"`
	Condition uint8  `json:"condition,omitempty"`
}

type StashManager struct {
	db           *database.DB
	mu           sync.RWMutex
	rmwMu        sync.Mutex
	openSessions map[uint32]uint32 // sessionID -> stashID
}

func NewStashManager(dbs ...*database.DB) *StashManager {
	mgr := &StashManager{
		openSessions: make(map[uint32]uint32),
	}
	if len(dbs) > 0 {
		mgr.db = dbs[0]
	}
	return mgr
}

func (m *StashManager) SetDB(db *database.DB) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.db = db
}

func (m *StashManager) getDB() (*database.DB, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.db == nil {
		return nil, ErrDBNotConfigured
	}
	return m.db, nil
}

// GetStash retrieves stash record by stashID.
func (m *StashManager) GetStash(stashID uint32) (*StashData, error) {
	db, err := m.getDB()
	if err != nil {
		return nil, err
	}

	rec, err := db.GetStash(stashID)
	if err != nil {
		return nil, ErrStashNotFound
	}

	return &StashData{
		StashID:   rec.StashID,
		LevelName: rec.LevelName,
		PosX:      rec.PosX,
		PosY:      rec.PosY,
		PosZ:      rec.PosZ,
		OwnerUUID: rec.OwnerUUID,
		Passcode:  rec.Passcode,
		Contents:  []byte(rec.ContentsJSON),
		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
	}, nil
}

// SaveStash inserts or updates a world stash.
func (m *StashManager) SaveStash(stashID uint32, level string, x, y, z float32, contents []byte) error {
	db, err := m.getDB()
	if err != nil {
		return err
	}

	contentsJSON := string(contents)
	if len(contents) == 0 {
		contentsJSON = "[]"
	}

	return db.SaveStash(stashID, level, x, y, z, contentsJSON)
}

// ValidateAccess enforces proximity, level, and passcode checks for a stash.
// It returns nil when playerPos/playerLevel/passcode are authorized to
// interact with stash, or a sentinel error otherwise:
//   - ErrStashNotFound when stash is nil
//   - ErrStashWrongLevel when levels differ
//   - ErrStashTooFar when 3D distance exceeds MaxStashInteractDistance (5m)
//   - ErrStashWrongPasscode when stash.Passcode != "" and does not match
func (m *StashManager) ValidateAccess(stash *database.StashRecord, playerPos [3]float32, playerLevel string, passcode string) error {
	if stash == nil {
		return ErrStashNotFound
	}
	if stash.LevelName != playerLevel {
		return ErrStashWrongLevel
	}
	dx := float64(playerPos[0] - stash.PosX)
	dy := float64(playerPos[1] - stash.PosY)
	dz := float64(playerPos[2] - stash.PosZ)
	if math.Sqrt(dx*dx+dy*dy+dz*dz) > MaxStashInteractDistance {
		return ErrStashTooFar
	}
	if stash.Passcode != "" && passcode != stash.Passcode {
		return ErrStashWrongPasscode
	}
	return nil
}

// OpenStash marks the stash as opened by sessionID and returns its contents.
func (m *StashManager) OpenStash(stashID uint32, sessionID uint32) ([]byte, error) {
	m.rmwMu.Lock()
	defer m.rmwMu.Unlock()

	stash, err := m.GetStash(stashID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.openSessions[sessionID] = stashID
	m.mu.Unlock()

	return stash.Contents, nil
}

// CloseStash clears the open state for sessionID.
func (m *StashManager) CloseStash(sessionID uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.openSessions, sessionID)
}

// GetOpenStash returns the stash currently opened by sessionID, if any.
func (m *StashManager) GetOpenStash(sessionID uint32) (uint32, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	stashID, ok := m.openSessions[sessionID]
	return stashID, ok
}

// ModifyStashItem adds or deducts item count in a stash.
// If countDelta > 0, adds count (or inserts item if not present).
// If countDelta < 0, deducts count (removes item if count reaches 0, errors if count would be < 0).
func (m *StashManager) ModifyStashItem(stashID uint32, itemSection string, countDelta int) error {
	db, err := m.getDB()
	if err != nil {
		return err
	}

	// Serialize read-modify-write to close TOCTOU races (concurrent Take/Store).
	m.rmwMu.Lock()
	defer m.rmwMu.Unlock()

	rec, err := db.GetStash(stashID)
	if err != nil {
		return ErrStashNotFound
	}

	var items []StashItem
	if len(rec.ContentsJSON) > 0 {
		if err := json.Unmarshal([]byte(rec.ContentsJSON), &items); err != nil {
			return err
		}
	}

	foundIndex := -1
	for i, it := range items {
		if it.Section == itemSection {
			foundIndex = i
			break
		}
	}

	if foundIndex >= 0 {
		newCount := items[foundIndex].Count + countDelta
		if newCount < 0 {
			return ErrInsufficientCount
		}
		if newCount == 0 {
			items = append(items[:foundIndex], items[foundIndex+1:]...)
		} else {
			items[foundIndex].Count = newCount
		}
	} else {
		if countDelta < 0 {
			return ErrItemNotFoundStash
		}
		if countDelta > 0 {
			items = append(items, StashItem{
				Section: itemSection,
				Count:   countDelta,
			})
		}
	}

	updatedBytes, err := json.Marshal(items)
	if err != nil {
		return err
	}

	return db.UpdateStashContents(stashID, string(updatedBytes))
}

func (m *StashManager) StoreStashItem(stashID uint32, ownerUUID, itemSection string, count int) error {
	if len(itemSection) == 0 || len(itemSection) > 32 {
		return ErrInvalidStashSection
	}
	for _, r := range itemSection {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return ErrInvalidStashSection
	}
	if count < 1 || count > 100 {
		return ErrInvalidStashCount
	}
	if ownerUUID == "" {
		return ErrItemNotFoundStash
	}
	db, err := m.getDB()
	if err != nil {
		return err
	}
	m.rmwMu.Lock()
	defer m.rmwMu.Unlock()
	tx, err := db.RawDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, condition FROM character_inventory WHERE client_uuid=? AND item_section=?`, ownerUUID, itemSection)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, count)
	for rows.Next() {
		var id int64
		var cond float64
		if err := rows.Scan(&id, &cond); err != nil {
			rows.Close()
			return err
		}
		if math.IsNaN(cond) || cond < 0 || cond > 1 {
			rows.Close()
			return ErrInvalidItemCondition
		}
		ids = append(ids, id)
		if len(ids) == count {
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(ids) == 0 {
		return ErrItemNotFoundStash
	}
	if len(ids) < count {
		return ErrInsufficientCount
	}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM character_inventory WHERE id=?`, id); err != nil {
			return err
		}
	}
	var contentsJSON string
	if err := tx.QueryRow(`SELECT contents_json FROM world_stashes WHERE stash_id=?`, stashID).Scan(&contentsJSON); err != nil {
		return ErrStashNotFound
	}
	var items []StashItem
	if len(contentsJSON) > 0 {
		if err := json.Unmarshal([]byte(contentsJSON), &items); err != nil {
			return err
		}
	}
	sanitized := make([]StashItem, 0, len(items)+1)
	for _, it := range items {
		if len(it.Section) == 0 || len(it.Section) > 32 || it.Count <= 0 {
			continue
		}
		valid := true
		for _, r := range it.Section {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
				continue
			}
			valid = false
			break
		}
		if !valid {
			continue
		}
		sanitized = append(sanitized, it)
	}
	foundIndex := -1
	for i, it := range sanitized {
		if it.Section == itemSection {
			foundIndex = i
			break
		}
	}
	if foundIndex >= 0 {
		sanitized[foundIndex].Count += count
	} else {
		sanitized = append(sanitized, StashItem{Section: itemSection, Count: count})
	}
	updatedBytes, err := json.Marshal(sanitized)
	if err != nil {
		return err
	}
	if len(updatedBytes) > 4096 || len(sanitized) > 256 {
		return ErrStashContentsTooLarge
	}
	if _, err := tx.Exec(`UPDATE world_stashes SET contents_json=?, updated_at=? WHERE stash_id=?`, string(updatedBytes), time.Now().Unix(), stashID); err != nil {
		return err
	}
	return tx.Commit()
}

// DepositItem moves count copies of section from the character inventory into
// the stash JSON in ONE transaction (OpContainerAction deposit). Stacks merge
// by section + condition bucket. Insufficient inventory returns
// database.ErrInsufficientItems and changes nothing.
func (m *StashManager) DepositItem(stashID uint32, ownerUUID, level, section string, count int, condition uint8) error {
	if count < 1 || count > 65535 {
		return ErrInvalidStashCount
	}
	db, err := m.getDB()
	if err != nil {
		return err
	}

	// Serialize read-modify-write against the other stash APIs.
	m.rmwMu.Lock()
	defer m.rmwMu.Unlock()

	tx, err := db.RawDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var levelName, contentsJSON string
	err = tx.QueryRow(`SELECT level_name, contents_json FROM world_stashes WHERE stash_id=?`, stashID).Scan(&levelName, &contentsJSON)
	if err == sql.ErrNoRows {
		return ErrStashNotFound
	}
	if err != nil {
		return err
	}
	if levelName != level {
		return ErrStashWrongLevel
	}

	rows, err := tx.Query(`SELECT id, item_count FROM character_inventory WHERE client_uuid=? AND item_section=? ORDER BY id`, ownerUUID, section)
	if err != nil {
		return err
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
			return err
		}
		held = append(held, r)
		total += r.count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if total < count {
		return database.ErrInsufficientItems
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
			if _, err := tx.Exec(`DELETE FROM character_inventory WHERE id=?`, r.id); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`UPDATE character_inventory SET item_count = item_count - ? WHERE id=?`, take, r.id); err != nil {
				return err
			}
		}
		remaining -= take
	}

	items, err := parseStashItems(contentsJSON)
	if err != nil {
		return err
	}
	if idx := findStashItem(items, section, condition); idx >= 0 {
		items[idx].Count += count
	} else {
		items = append(items, StashItem{Section: section, Count: count, Condition: condition})
	}
	updatedBytes, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if len(updatedBytes) > 4096 || len(items) > 256 {
		return ErrStashContentsTooLarge
	}
	if _, err := tx.Exec(`UPDATE world_stashes SET contents_json=?, updated_at=? WHERE stash_id=?`, string(updatedBytes), time.Now().Unix(), stashID); err != nil {
		return err
	}
	return tx.Commit()
}

// WithdrawItem moves count copies of section (matching condition bucket) out of
// the stash JSON and credits the character inventory in ONE transaction
// (OpContainerAction withdraw). Missing entries return ErrItemNotFoundStash,
// short stacks ErrInsufficientCount, and neither changes state.
func (m *StashManager) WithdrawItem(stashID uint32, ownerUUID, level, section string, count int, condition uint8) error {
	if count < 1 || count > 65535 {
		return ErrInvalidStashCount
	}
	db, err := m.getDB()
	if err != nil {
		return err
	}

	m.rmwMu.Lock()
	defer m.rmwMu.Unlock()

	tx, err := db.RawDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var levelName, contentsJSON string
	err = tx.QueryRow(`SELECT level_name, contents_json FROM world_stashes WHERE stash_id=?`, stashID).Scan(&levelName, &contentsJSON)
	if err == sql.ErrNoRows {
		return ErrStashNotFound
	}
	if err != nil {
		return err
	}
	if levelName != level {
		return ErrStashWrongLevel
	}

	items, err := parseStashItems(contentsJSON)
	if err != nil {
		return err
	}
	idx := findStashItem(items, section, condition)
	if idx < 0 {
		return ErrItemNotFoundStash
	}
	if items[idx].Count < count {
		return ErrInsufficientCount
	}
	items[idx].Count -= count
	if items[idx].Count == 0 {
		items = append(items[:idx], items[idx+1:]...)
	}
	updatedBytes, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE world_stashes SET contents_json=?, updated_at=? WHERE stash_id=?`, string(updatedBytes), time.Now().Unix(), stashID); err != nil {
		return err
	}

	// Credit the owner's inventory, merging into an existing stack of the same
	// section when one exists (mirrors PickupWorldItem).
	var invID int
	err = tx.QueryRow(`SELECT id FROM character_inventory WHERE client_uuid=? AND item_section=? ORDER BY id LIMIT 1`, ownerUUID, section).Scan(&invID)
	switch {
	case err == sql.ErrNoRows:
		cond := float64(condition) / 100.0
		if _, err := tx.Exec(`INSERT INTO character_inventory (client_uuid, item_section, item_count, condition) VALUES (?, ?, ?, ?)`,
			ownerUUID, section, count, cond); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if _, err := tx.Exec(`UPDATE character_inventory SET item_count = item_count + ? WHERE id=?`, count, invID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// parseStashItems decodes a stash contents_json string into items, tolerating
// empty/NULL contents.
func parseStashItems(contentsJSON string) ([]StashItem, error) {
	if contentsJSON == "" {
		return nil, nil
	}
	var items []StashItem
	if err := json.Unmarshal([]byte(contentsJSON), &items); err != nil {
		return nil, err
	}
	return items, nil
}

// findStashItem returns the index of the section + condition bucket entry, or
// -1 when absent. Entries written before the condition field existed match
// bucket 0.
func findStashItem(items []StashItem, section string, condition uint8) int {
	for i := range items {
		if items[i].Section == section && items[i].Condition == condition {
			return i
		}
	}
	return -1
}
