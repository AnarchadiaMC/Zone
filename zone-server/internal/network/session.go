package network

import (
	"net"
	"sync"
	"time"
)

type PlayerSession struct {
	sync.Mutex
	SessionID         uint32
	AccountID         string
	UDPAddr           *net.UDPAddr
	CurrentLevel      string
	Position          [3]float32
	Rotation          [2]float32
	Velocity          [3]float32
	Health            float32
	AnimFlags         uint8
	InSafeZone        bool
	SafeZoneID        string
	LastSeen          time.Time
	LastSequence      uint32
	LastTransformTime time.Time
	LastRejectedPos   [3]float32
	RejectConfirm     int
	HasRejected       bool
	SessionToken      uint64
	Dirty             bool
	LastCheckpoint    time.Time
	InCombatUntil     time.Time
	ChatTimestamps    []time.Time
}

// AllowChat enforces per-session chat rate limiting (max 5 msgs / 5s).
// Prunes timestamps older than 5s; returns false (drop silently) if the
// session already sent 5 messages in the window, otherwise records now
// and returns true.
func (s *PlayerSession) AllowChat(now time.Time) bool {
	s.Lock()
	defer s.Unlock()
	cutoff := now.Add(-5 * time.Second)
	kept := s.ChatTimestamps[:0]
	for _, t := range s.ChatTimestamps {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.ChatTimestamps = kept
	if len(s.ChatTimestamps) >= 5 {
		return false
	}
	s.ChatTimestamps = append(s.ChatTimestamps, now)
	return true
}

func (s *PlayerSession) MarkDirty() {
	s.Lock()
	defer s.Unlock()
	s.Dirty = true
}

func (s *PlayerSession) SetCombat(duration time.Duration) {
	s.Lock()
	defer s.Unlock()
	s.InCombatUntil = time.Now().Add(duration)
}

func (s *PlayerSession) IsInCombat(now time.Time) bool {
	s.Lock()
	defer s.Unlock()
	return now.Before(s.InCombatUntil)
}

type SessionManager struct {
	mu        sync.RWMutex
	sessions  map[uint32]*PlayerSession
	byAddr    map[string]*PlayerSession
	allCached []*PlayerSession
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[uint32]*PlayerSession),
		byAddr:   make(map[string]*PlayerSession),
	}
}

func (sm *SessionManager) updateCache() {
	all := make([]*PlayerSession, 0, len(sm.sessions))
	for _, s := range sm.sessions {
		all = append(all, s)
	}
	sm.allCached = all
}

func (sm *SessionManager) AddSession(s *PlayerSession) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if old, exists := sm.sessions[s.SessionID]; exists && old != nil {
		if old.UDPAddr != nil {
			delete(sm.byAddr, old.UDPAddr.String())
		}
	}
	sm.sessions[s.SessionID] = s
	if s.UDPAddr != nil {
		sm.byAddr[s.UDPAddr.String()] = s
	}
	sm.updateCache()
}

// TryAddCapped performs check-and-insert under ONE mutex hold, closing the
// TOCTOU window between GetAll()+AddSession across UDP workers.
// Returns false (caller sends Status 1) when len(sessions) >= max.
// Duplicate connections from the same addr replace the old entry instead of
// counting twice: old sessions with the same addr (different SessionID) are
// removed first. Same-SessionID re-adds are treated as updates and allowed
// even at cap. max <= 0 means uncapped (always insert).
func (sm *SessionManager) TryAddCapped(s *PlayerSession, max int) bool {
	if s == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s.UDPAddr != nil {
		addrStr := s.UDPAddr.String()
		if _, ok := sm.byAddr[addrStr]; ok {
			for id, sess := range sm.sessions {
				if id == s.SessionID {
					continue
				}
				if sess != nil && sess.UDPAddr != nil && sess.UDPAddr.String() == addrStr {
					delete(sm.sessions, id)
				}
			}
		}
	}
	if max > 0 && len(sm.sessions) >= max {
		if _, exists := sm.sessions[s.SessionID]; !exists {
			return false
		}
	}
	// Reuse AddSession dedup semantics: clean stale byAddr for same SessionID.
	if old, exists := sm.sessions[s.SessionID]; exists && old != nil && old != s {
		if old.UDPAddr != nil {
			delete(sm.byAddr, old.UDPAddr.String())
		}
	}
	sm.sessions[s.SessionID] = s
	if s.UDPAddr != nil {
		sm.byAddr[s.UDPAddr.String()] = s
	}
	sm.updateCache()
	return true
}

func (sm *SessionManager) RemoveSession(id uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s, ok := sm.sessions[id]; ok {
		delete(sm.sessions, id)
		if s.UDPAddr != nil {
			delete(sm.byAddr, s.UDPAddr.String())
		}
		sm.updateCache()
	}
}

func (sm *SessionManager) GetByID(id uint32) *PlayerSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessions[id]
}

func (sm *SessionManager) GetByAddr(addr string) *PlayerSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.byAddr[addr]
}

func (sm *SessionManager) GetAll() []*PlayerSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.allCached
}

func (sm *SessionManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}
