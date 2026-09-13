package game

import (
	"encoding/json"
	"errors"
	"sync"

	"zone-online/zone-server/internal/database"
)

var (
	ErrStashNotFound     = errors.New("stash not found")
	ErrItemNotFoundStash = errors.New("item not found in stash")
	ErrInsufficientCount = errors.New("insufficient item count in stash")
)

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

type StashItem struct {
	Section string `json:"section"`
	Count   int    `json:"count"`
}

type StashManager struct {
	db           *database.DB
	mu           sync.RWMutex
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


// OpenStash marks the stash as opened by sessionID and returns its contents.
func (m *StashManager) OpenStash(stashID uint32, sessionID uint32) ([]byte, error) {
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
