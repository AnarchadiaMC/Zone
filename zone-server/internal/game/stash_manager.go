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
	ErrInvalidStashCount     = errors.New("invalid stash count")
	ErrInvalidItemCondition  = errors.New("invalid item condition")
	ErrStashContentsTooLarge = errors.New("stash contents too large")
)

// MaxStashInteractDistance is the maximum 3D distance (meters) allowed
// between a player and a stash for any interaction.
const MaxStashInteractDistance = 5.0

// WorldStashGridM is the side of the square grid cell used to key the
// position-addressed world stashes of OpStashAction. The Lua sender transmits
// raw float coordinates; the server is the single authority that rounds them.
// Rounding formula (mirror in Lua): grid = 0.5,
//
//	key = math.floor(v / 0.5 + 0.5) * 0.5
//
// i.e. round-half-toward-positive-infinity for every sign. NaN/Inf maps to 0.
const WorldStashGridM = 0.5

// roundWorldStashCoord rounds one coordinate onto the 0.5 m world-stash grid.
func roundWorldStashCoord(v float32) float32 {
	f := float64(v)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return float32(math.Floor(f/WorldStashGridM+0.5) * WorldStashGridM)
}

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
// 0-100 wire bucket used by every deposit/withdraw merge; rows without the
// field decode to bucket 0.
type StashItem struct {
	Section   string `json:"section"`
	Count     int    `json:"count"`
	Condition uint8  `json:"condition,omitempty"`
}

type StashManager struct {
	db    *database.DB
	mu    sync.RWMutex
	rmwMu sync.Mutex
}

func NewStashManager(dbs ...*database.DB) *StashManager {
	mgr := &StashManager{}
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

// FindWorldStash resolves the stash stored at the (already rounded) level +
// position key of OpStashAction. A missing row maps onto ErrStashNotFound.
func (m *StashManager) FindWorldStash(level string, x, y, z float32) (*database.StashRecord, error) {
	db, err := m.getDB()
	if err != nil {
		return nil, err
	}
	rec, err := db.FindStashAt(level, x, y, z)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrStashNotFound
		}
		return nil, err
	}
	return rec, nil
}

// FindOrCreateWorldStash resolves the stash at the (already rounded) key,
// creating an ownerless world stash on the first store. The create is
// serialized with the other stash APIs by rmwMu so two concurrent first
// stores cannot materialise duplicate stashes for one key.
func (m *StashManager) FindOrCreateWorldStash(level string, x, y, z float32) (*database.StashRecord, error) {
	db, err := m.getDB()
	if err != nil {
		return nil, err
	}
	m.rmwMu.Lock()
	defer m.rmwMu.Unlock()
	return db.FindOrCreateStashAt(level, x, y, z)
}

// ValidateAccess enforces ownership, proximity, level, and passcode checks for
// a stash. It returns nil when the requester is authorized to interact with
// stash, or a sentinel error otherwise:
//   - ErrStashNotFound when stash is nil
//   - ErrStashWrongLevel when levels differ
//   - ErrStashTooFar when 3D distance exceeds MaxStashInteractDistance (5m)
//   - ErrStashWrongPasscode when stash.Passcode != "" and does not match
//   - ErrStashAccessDenied when the stash has an owner_uuid that is
//     neither empty nor the requesting character
//
// An empty/NULL owner_uuid means an ownerless world stash: anyone within range
// is allowed. Personal stashes are bound to their owning character.
func (m *StashManager) ValidateAccess(stash *database.StashRecord, playerPos [3]float32, playerLevel string, passcode string, requesterUUID string) error {
	if stash == nil {
		return ErrStashNotFound
	}
	if stash.OwnerUUID != "" && stash.OwnerUUID != requesterUUID {
		return ErrStashAccessDenied
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

// DepositItem moves count copies of section from the character inventory into
// the stash JSON in ONE transaction (OpContainerAction deposit). Stacks merge
// by section + condition bucket; the stored bucket is derived from the
// inventory row actually consumed (preferredBucket 0..100 only chooses which
// stack is consumed first), never from a client-claimed condition, so a 5%
// item cannot launder itself onto a 100% stack. It returns the stored
// condition bucket. Insufficient inventory returns
// database.ErrInsufficientItems and changes nothing.
func (m *StashManager) DepositItem(stashID uint32, ownerUUID, level, section string, count int, preferredBucket int) (uint8, error) {
	if count < 1 || count > 65535 {
		return 0, ErrInvalidStashCount
	}
	if preferredBucket < 0 || preferredBucket > 100 {
		return 0, ErrInvalidItemCondition
	}
	db, err := m.getDB()
	if err != nil {
		return 0, err
	}

	// Serialize read-modify-write against the other stash APIs.
	m.rmwMu.Lock()
	defer m.rmwMu.Unlock()

	tx, err := db.RawDB().Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var levelName, contentsJSON string
	err = tx.QueryRow(`SELECT level_name, contents_json FROM world_stashes WHERE stash_id=?`, stashID).Scan(&levelName, &contentsJSON)
	if err == sql.ErrNoRows {
		return 0, ErrStashNotFound
	}
	if err != nil {
		return 0, err
	}
	if levelName != level {
		return 0, ErrStashWrongLevel
	}

	_, removedCondition, err := database.RemoveInventoryItemsLocked(tx, ownerUUID, section, count, preferredBucket)
	if err != nil {
		return 0, err
	}
	condition := conditionToWire(removedCondition)

	items, err := parseStashItems(contentsJSON)
	if err != nil {
		return 0, err
	}
	if idx := findStashItem(items, section, condition); idx >= 0 {
		items[idx].Count += count
	} else {
		items = append(items, StashItem{Section: section, Count: count, Condition: condition})
	}
	updatedBytes, err := json.Marshal(items)
	if err != nil {
		return 0, err
	}
	if len(updatedBytes) > 4096 || len(items) > 256 {
		return 0, ErrStashContentsTooLarge
	}
	if _, err := tx.Exec(`UPDATE world_stashes SET contents_json=?, updated_at=? WHERE stash_id=?`, string(updatedBytes), time.Now().Unix(), stashID); err != nil {
		return 0, err
	}
	return condition, tx.Commit()
}

// WithdrawItem moves count copies of section out of the stash JSON and credits
// the character inventory in ONE transaction (OpContainerAction withdraw).
// condition 0..100 matches that bucket exactly. The credited inventory row
// receives the stash bucket's own condition, never a client byte. It returns
// the condition bucket withdrawn. Missing entries return ErrItemNotFoundStash,
// short stacks ErrInsufficientCount, and neither changes state.
func (m *StashManager) WithdrawItem(stashID uint32, ownerUUID, level, section string, count int, condition int) (uint8, error) {
	if count < 1 || count > 65535 {
		return 0, ErrInvalidStashCount
	}
	if condition < 0 || condition > 100 {
		return 0, ErrInvalidItemCondition
	}
	db, err := m.getDB()
	if err != nil {
		return 0, err
	}

	m.rmwMu.Lock()
	defer m.rmwMu.Unlock()

	tx, err := db.RawDB().Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var levelName, contentsJSON string
	err = tx.QueryRow(`SELECT level_name, contents_json FROM world_stashes WHERE stash_id=?`, stashID).Scan(&levelName, &contentsJSON)
	if err == sql.ErrNoRows {
		return 0, ErrStashNotFound
	}
	if err != nil {
		return 0, err
	}
	if levelName != level {
		return 0, ErrStashWrongLevel
	}

	items, err := parseStashItems(contentsJSON)
	if err != nil {
		return 0, err
	}
	idx := findStashItem(items, section, uint8(condition))
	if idx < 0 {
		return 0, ErrItemNotFoundStash
	}
	if items[idx].Count < count {
		return 0, ErrInsufficientCount
	}
	bucket := items[idx].Condition
	items[idx].Count -= count
	if items[idx].Count == 0 {
		items = append(items[:idx], items[idx+1:]...)
	}
	updatedBytes, err := json.Marshal(items)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE world_stashes SET contents_json=?, updated_at=? WHERE stash_id=?`, string(updatedBytes), time.Now().Unix(), stashID); err != nil {
		return 0, err
	}

	// Credit the owner's inventory using the stash bucket's condition (merge by
	// section + bucket; single-object ammo-bearing rows receive rounds).
	if err := database.CreditInventoryLocked(tx, ownerUUID, section, count, bucket); err != nil {
		return 0, err
	}

	return bucket, tx.Commit()
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
