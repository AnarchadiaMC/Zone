package game

import (
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/network"
)

// Sleeper represents a proxy entity left behind in the Zone when a player disconnects
// outside a safe zone or while recently engaged in combat, preventing combat logging.
type Sleeper struct {
	sync.RWMutex
	EntityID     uint32
	AccountID    string
	Position     [3]float32
	Rotation     [2]float32
	Health       float32
	CurrentLevel string
	ExpiresAt    time.Time
	IsDead       bool
}

// SleeperManager tracks and coordinates active sleeper proxy entities across the Zone.
type SleeperManager struct {
	mu        sync.RWMutex
	sleepers  map[uint32]*Sleeper
	byAccount map[string]*Sleeper
	logger    *zap.Logger
	seq       atomic.Uint32
}

// NewSleeperManager initializes an empty sleeper manager.
func NewSleeperManager(logger ...*zap.Logger) *SleeperManager {
	mgr := &SleeperManager{
		sleepers:  make(map[uint32]*Sleeper),
		byAccount: make(map[string]*Sleeper),
	}
	if len(logger) > 0 && logger[0] != nil {
		mgr.logger = logger[0]
	} else {
		mgr.logger = zap.NewNop()
	}
	return mgr
}

// CreateSleeper extracts player session state and spawns a temporary proxy sleeper entity
// that expires after the specified duration.
func (m *SleeperManager) CreateSleeper(sess *network.PlayerSession, duration time.Duration) *Sleeper {
	if sess == nil {
		return nil
	}

	sess.Lock()
	pos := sess.Position
	rot := sess.Rotation
	health := sess.Health
	level := sess.CurrentLevel
	accountID := sess.AccountID
	sessionID := sess.SessionID
	sess.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	entityID := sessionID
	if entityID == 0 {
		for {
			candidate := m.seq.Add(1)
			if _, exists := m.sleepers[candidate]; !exists {
				entityID = candidate
				break
			}
		}
	}

	// Clean up any stale sleeper proxy for this account
	if accountID != "" {
		if old, exists := m.byAccount[accountID]; exists {
			delete(m.sleepers, old.EntityID)
			delete(m.byAccount, accountID)
		}
	}
	// Clean up any existing sleeper with the same entity ID
	if old, exists := m.sleepers[entityID]; exists {
		delete(m.sleepers, entityID)
		if old.AccountID != "" {
			delete(m.byAccount, old.AccountID)
		}
	}

	sleeper := &Sleeper{
		EntityID:     entityID,
		AccountID:    accountID,
		Position:     pos,
		Rotation:     rot,
		Health:       health,
		CurrentLevel: level,
		ExpiresAt:    time.Now().Add(duration),
		IsDead:       false,
	}

	m.sleepers[entityID] = sleeper
	if accountID != "" {
		m.byAccount[accountID] = sleeper
	}

	if m.logger != nil {
		m.logger.Info("Created sleeper proxy entity",
			zap.Uint32("entity_id", entityID),
			zap.String("account_id", accountID),
			zap.String("level", level),
			zap.Float32("health", health),
			zap.Duration("duration", duration),
		)
	}

	return sleeper
}

// GetSleeper finds an active sleeper proxy by entity ID.
func (m *SleeperManager) GetSleeper(entityID uint32) *Sleeper {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sleepers[entityID]
}

// GetSleeperByAccount finds an active sleeper proxy by account ID.
func (m *SleeperManager) GetSleeperByAccount(accountID string) *Sleeper {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byAccount[accountID]
}

// RemoveSleeper unregisters a sleeper proxy entity by entity ID.
func (m *SleeperManager) RemoveSleeper(entityID uint32) *Sleeper {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, exists := m.sleepers[entityID]
	if !exists {
		return nil
	}

	delete(m.sleepers, entityID)
	if s.AccountID != "" {
		if cur, ok := m.byAccount[s.AccountID]; ok && cur == s {
			delete(m.byAccount, s.AccountID)
		}
	}

	if m.logger != nil {
		m.logger.Info("Removed sleeper proxy entity",
			zap.Uint32("entity_id", entityID),
			zap.String("account_id", s.AccountID),
		)
	}

	return s
}

// ApplyDamage safely applies incoming damage to a sleeper proxy.
// If health drops to 0 or below, IsDead is marked true and health is clamped to 0.
func (m *SleeperManager) ApplyDamage(entityID uint32, damage float32) (remainingHealth float32, isDead bool) {
	m.mu.RLock()
	s := m.sleepers[entityID]
	m.mu.RUnlock()

	if s == nil {
		return 0, false
	}

	s.Lock()
	defer s.Unlock()

	if s.IsDead {
		return 0, true
	}

	if damage > 0 {
		s.Health -= damage
	}

	if s.Health <= 0 {
		s.Health = 0
		s.IsDead = true
	}

	if m.logger != nil {
		m.logger.Debug("Applied damage to sleeper",
			zap.Uint32("entity_id", entityID),
			zap.Float32("damage", damage),
			zap.Float32("remaining_health", s.Health),
			zap.Bool("is_dead", s.IsDead),
		)
	}

	return s.Health, s.IsDead
}

// Tick evaluates all active sleeper proxies.
// If a sleeper is dead, it is removed and onDeath is invoked.
// Else if it has expired past its duration, it is removed and onExpire is invoked.
func (m *SleeperManager) Tick(now time.Time, onExpire func(s *Sleeper), onDeath func(s *Sleeper)) {
	m.mu.Lock()
	var toDeath []*Sleeper
	var toExpire []*Sleeper

	for id, s := range m.sleepers {
		s.RLock()
		isDead := s.IsDead
		isExpired := now.After(s.ExpiresAt) || now.Equal(s.ExpiresAt)
		s.RUnlock()

		if isDead {
			toDeath = append(toDeath, s)
			delete(m.sleepers, id)
			if s.AccountID != "" {
				if cur, ok := m.byAccount[s.AccountID]; ok && cur == s {
					delete(m.byAccount, s.AccountID)
				}
			}
		} else if isExpired {
			toExpire = append(toExpire, s)
			delete(m.sleepers, id)
			if s.AccountID != "" {
				if cur, ok := m.byAccount[s.AccountID]; ok && cur == s {
					delete(m.byAccount, s.AccountID)
				}
			}
		}
	}
	m.mu.Unlock()

	for _, s := range toDeath {
		if m.logger != nil {
			m.logger.Info("Sleeper entity killed in combat",
				zap.Uint32("entity_id", s.EntityID),
				zap.String("account_id", s.AccountID),
			)
		}
		if onDeath != nil {
			onDeath(s)
		}
	}

	for _, s := range toExpire {
		if m.logger != nil {
			m.logger.Info("Sleeper entity timer expired",
				zap.Uint32("entity_id", s.EntityID),
				zap.String("account_id", s.AccountID),
			)
		}
		if onExpire != nil {
			onExpire(s)
		}
	}
}

// GetAll returns a slice copy of all currently active sleeper proxies.
func (m *SleeperManager) GetAll() []*Sleeper {
	m.mu.RLock()
	defer m.mu.RUnlock()

	all := make([]*Sleeper, 0, len(m.sleepers))
	for _, s := range m.sleepers {
		all = append(all, s)
	}
	return all
}

// Count returns the number of currently active sleeper proxies.
func (m *SleeperManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sleepers)
}
