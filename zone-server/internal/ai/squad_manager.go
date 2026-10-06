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
	// Combat FSM extras: Alert is the post-chase cool-down state, Chase is a
	// run-speed pursuit of a hostile player.
	AIStateAlert AIState = 5
	AIStateChase AIState = 6
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
	case AIStateAlert:
		return "Alert"
	case AIStateChase:
		return "Chase"
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
	// TickPuppets along Loop and replicated to clients through OpAIState.
	// Wave B adds an optional combat FSM (TickPuppetsWithCombat): Patrol/Idle
	// acquire a hostile player inside the aggro radius, Chase pursues at run
	// speed, Attack lands melee hits on the cooldown, and Alert is the
	// post-chase cool-down before returning to Patrol.
	Puppet       bool
	Label        string
	Section      string
	PatrolRadius float32
	WalkSpeed    float32 // m/s, used while patrolling (default 1.5)
	RunSpeed     float32 // m/s, used while runUntil is in the future or chasing (default 3.0)
	Yaw          float32 // radians, faces the current movement direction
	Anim         uint8   // AnimIdle/AnimWalk/AnimRun/AnimAttack/AnimDeath wire value
	Online       bool    // true when a player is within the replication radius
	Loop         []Waypoint
	LoopIndex    int
	lastStep     time.Time
	runUntil     time.Time
	// Combat FSM state, only advanced by TickPuppetsWithCombat.
	TargetID   uint32    // current hostile player session id, 0 when none
	LastAttack time.Time // last accepted melee swing
	AlertUntil time.Time // Alert -> Patrol resume instant
	// deadSince records when the squad entered AIStateDead; TickPuppets keeps
	// broadcasting AnimDeath for the corpse delay before despawning it.
	deadSince time.Time
}

// SquadManager manages all active AI squads.
type SquadManager struct {
	mu     sync.RWMutex
	squads map[uint32]*Squad
	nextID uint32
	// corpseDelay overrides puppetDespawnDelay when > 0 (config
	// ai_corpse_seconds). Set once at construction from the game package.
	corpseDelay time.Duration
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

// SetCorpseDelay overrides how long a dead puppet keeps broadcasting AnimDeath
// before it is despawned. Values <= 0 fall back to puppetDespawnDelay (5 s).
func (sm *SquadManager) SetCorpseDelay(d time.Duration) {
	sm.mu.Lock()
	sm.corpseDelay = d
	sm.mu.Unlock()
}

// GetPuppetState returns a value snapshot of one puppet squad (including its
// combat state), so callers outside the manager lock can inspect it safely.
func (sm *SquadManager) GetPuppetState(id uint32) (PuppetState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	sq, ok := sm.squads[id]
	if !ok || !sq.Puppet {
		return PuppetState{}, false
	}
	return puppetStateOf(sq), true
}

// ApplyPuppetDamage subtracts damage from a puppet's health. On lethal damage
// the squad transitions to AIStateDead (the next tick streams AnimDeath and the
// corpse is released after the corpse delay). It returns the remaining health
// and whether the puppet died.
func (sm *SquadManager) ApplyPuppetDamage(id uint32, damage float32) (health float32, dead bool, ok bool) {
	if damage < 0 {
		damage = 0
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sq, found := sm.squads[id]
	if !found || !sq.Puppet || sq.State == AIStateDead {
		return 0, false, false
	}
	sq.Health -= damage
	if sq.Health <= 0 {
		sq.Health = 0
		sq.State = AIStateDead
		sq.TargetID = 0
		return 0, true, true
	}
	return sq.Health, false, true
}
