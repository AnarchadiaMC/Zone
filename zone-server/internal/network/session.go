package network

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type PlayerSession struct {
	sync.Mutex
	SessionID         uint32
	AccountID         string
	Name              string
	UDPAddr           *net.UDPAddr
	CurrentLevel      string
	Position          [3]float32
	Rotation          [2]float32
	Velocity          [3]float32
	Health            float32
	AnimFlags         uint8
	Gvid              uint16
	Visual            [64]byte
	HasVisual         bool
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
	PendingCreate     bool
	Faction           string
	// EnterBroadcastLevel is the level for which ENTITY_ENTER_AOI has been
	// broadcast to same-level peers. Empty until a broadcast happens with a
	// valid gvid and visual; reset on level change.
	EnterBroadcastLevel string
	// EnterBroadcastRevision is the VisualRevision that was current when the
	// last ENTITY_ENTER broadcast happened. A visual change bumps
	// VisualRevision and forces a re-broadcast to same-level peers.
	EnterBroadcastRevision uint64
	// VisualRevision increments each time the session's actor visual changes.
	VisualRevision uint64
	// InventorySyncedLevel is the level name for which character_inventory has
	// already been streamed to this client. Empty until the first post-spawn
	// OpLevelChange arrives, so a Lua VM restart cannot lose the starter kit.
	InventorySyncedLevel string
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
// It returns the sessions it evicted (same-address reconnect and same-ID
// replacement) so the caller can run full disconnect cleanup on them, and
// whether the new session was inserted. A false bool means the caller must
// still clean up any returned evicted sessions.
// Duplicate connections from the same addr replace the old entry instead of
// counting twice: old sessions with the same addr (different SessionID) are
// removed first. Same-SessionID re-adds are treated as updates and allowed
// even at cap. max <= 0 means uncapped (always insert).
func (sm *SessionManager) TryAddCapped(s *PlayerSession, max int) (evicted []*PlayerSession, added bool) {
	if s == nil {
		return nil, false
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
					evicted = append(evicted, sess)
				}
			}
		}
	}
	if max > 0 && len(sm.sessions) >= max {
		if _, exists := sm.sessions[s.SessionID]; !exists {
			return evicted, false
		}
	}
	// Reuse AddSession dedup semantics: clean stale byAddr for same SessionID.
	if old, exists := sm.sessions[s.SessionID]; exists && old != nil && old != s {
		evicted = append(evicted, old)
		if old.UDPAddr != nil {
			delete(sm.byAddr, old.UDPAddr.String())
		}
	}
	sm.sessions[s.SessionID] = s
	if s.UDPAddr != nil {
		sm.byAddr[s.UDPAddr.String()] = s
	}
	sm.updateCache()
	return evicted, true
}

// EnsureUniqueName makes s.Name case-insensitively unique across the current
// sessions, suffixing "~2", "~3", ... until free. Suffixes are deterministic
// and the result is capped at 31 bytes (UTF-8 safe) so the name always fits a
// 32-byte wire field with a trailing NUL. The caller must serialize concurrent
// handshakes; s must already be inserted so it is skipped during the scan.
func (sm *SessionManager) EnsureUniqueName(s *PlayerSession) {
	if s == nil {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s.Lock()
	base := s.Name
	s.Unlock()
	if base == "" {
		base = "Stalker"
	}

	taken := make(map[string]bool, len(sm.sessions))
	for _, other := range sm.sessions {
		if other == nil || other == s {
			continue
		}
		other.Lock()
		name := other.Name
		other.Unlock()
		taken[strings.ToLower(name)] = true
	}
	if !taken[strings.ToLower(base)] {
		s.Lock()
		s.Name = base
		s.Unlock()
		return
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("~%d", n)
		candidate := truncateUTF8(base, 31-len(suffix)) + suffix
		if !taken[strings.ToLower(candidate)] {
			s.Lock()
			s.Name = candidate
			s.Unlock()
			return
		}
	}
}

// truncateUTF8 cuts s to at most maxBytes without splitting a multi-byte rune.
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
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

// GetByName returns the connected session whose nickname matches
// case-insensitively, or nil when no session carries that name. Names are
// made unique at handshake time, so the match resolves to exactly one
// displayed name. Used by group invitations.
func (sm *SessionManager) GetByName(name string) *PlayerSession {
	if name == "" {
		return nil
	}
	sm.mu.RLock()
	all := sm.allCached
	sm.mu.RUnlock()
	for _, sess := range all {
		sess.Lock()
		match := strings.EqualFold(sess.Name, name)
		sess.Unlock()
		if match {
			return sess
		}
	}
	return nil
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
