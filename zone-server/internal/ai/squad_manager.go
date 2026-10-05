package ai

import (
	"sync"
	"time"
)

// AIState represents what the squad is currently doing.
type AIState uint8

const (
	AIStateIdle   AIState = 0
	AIStatePatrol AIState = 1
	AIStateAttack AIState = 2
	AIStateFlee   AIState = 3
	AIStateDead   AIState = 4
)

func (s AIState) String() string {
	switch s {
	case AIStateIdle:
		return "Idle"
	case AIStatePatrol:
		return "Patrol"
	case AIStateAttack:
		return "Attack"
	case AIStateFlee:
		return "Flee"
	case AIStateDead:
		return "Dead"
	default:
		return "Unknown"
	}
}

// Squad represents a group of AI entities moving through the Zone.
type Squad struct {
	ID       uint32
	DBID     int64      // ai_squads.squad_id when loaded from the DB, else 0
	Level    string     // level the squad occupies
	Position [3]float32 // current world-space position
	State    AIState
	Health   float32
	Faction  string

	// Puppet replication metadata (wave A). Puppet squads are simulated by
	// TickPuppets along Loop and replicated to clients through OpAIState. No
	// combat AI exists in this wave: State stays AIStatePatrol and damage is
	// never applied to puppets.
	Puppet       bool
	Label        string
	Section      string
	PatrolRadius float32
	WalkSpeed    float32 // m/s, used while patrolling (default 1.5)
	RunSpeed     float32 // m/s, used while runUntil is in the future (default 3.0)
	Yaw          float32 // radians, faces the current movement direction
	Anim         uint8   // AnimIdle/AnimWalk/AnimRun wire value
	Online       bool    // true when a player is within the replication radius
	Loop         []Waypoint
	LoopIndex    int
	lastStep     time.Time
	runUntil     time.Time
	// deadSince records when the squad entered AIStateDead; TickPuppets keeps
	// broadcasting AnimDeath for puppetDespawnDelay before despawning it.
	deadSince time.Time
}

// SquadManager manages all active AI squads.
type SquadManager struct {
	mu     sync.RWMutex
	squads map[uint32]*Squad
	nextID uint32
}

// NewSquadManager returns an initialised SquadManager.
func NewSquadManager() *SquadManager {
	return &SquadManager{
		squads: make(map[uint32]*Squad),
		nextID: 1,
	}
}

// DespawnSquad removes a squad by ID.
func (sm *SquadManager) DespawnSquad(id uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.squads, id)
}

// GetSquad returns a squad by ID and whether it was found.
func (sm *SquadManager) GetSquad(id uint32) (*Squad, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	sq, ok := sm.squads[id]
	return sq, ok
}

// SetSquadState updates the AI state of the squad with the given ID.
func (sm *SquadManager) SetSquadState(id uint32, state AIState) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sq, ok := sm.squads[id]; ok {
		sq.State = state
	}
}
