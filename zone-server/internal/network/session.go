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
	InSafeZone        bool
	SafeZoneID        string
	LastSeen          time.Time
	LastSequence      uint32
	LastTransformTime time.Time
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[uint32]*PlayerSession
	byAddr   map[string]*PlayerSession
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[uint32]*PlayerSession),
		byAddr:   make(map[string]*PlayerSession),
	}
}

func (sm *SessionManager) AddSession(s *PlayerSession) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sessions[s.SessionID] = s
	sm.byAddr[s.UDPAddr.String()] = s
}

func (sm *SessionManager) RemoveSession(id uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if s, ok := sm.sessions[id]; ok {
		delete(sm.sessions, id)
		delete(sm.byAddr, s.UDPAddr.String())
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
	var all []*PlayerSession
	for _, s := range sm.sessions {
		all = append(all, s)
	}
	return all
}

func (sm *SessionManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}
