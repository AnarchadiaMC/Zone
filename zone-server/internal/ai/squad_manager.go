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

// patrolStepSize is the world-unit distance advanced per Tick along a patrol path.
const patrolStepSize float32 = 2.0

// patrolRadius is the half-extent of the grid used to generate patrol waypoints.
const patrolRadius float32 = 32.0

// Squad represents a group of AI entities moving through the Zone.
type Squad struct {
	ID        uint32
	Level     string     // level the squad occupies
	Position  [3]float32 // current world-space position
	State     AIState
	Path      []Waypoint // waypoints produced by A* pathfinding
	PathIndex int        // next waypoint to move toward
	Health    float32
	Faction   string
	SpawnedAt time.Time
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

// SpawnSquad creates a new squad at the given world-space position and
// immediately computes a simple patrol circuit using A*.
func (sm *SquadManager) SpawnSquad(level string, x, y, z float32, faction string) *Squad {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	id := sm.nextID
	sm.nextID++

	start := Waypoint{X: x, Y: y, Z: z}
	goal := Waypoint{X: x + patrolRadius, Y: y, Z: z + patrolRadius}

	g := newPatrolGrid(start, goal)
	path := AStar(start, goal, g)

	// Fall back to a two-waypoint path when A* cannot find a route.
	if len(path) == 0 {
		path = []Waypoint{start, goal}
	}

	sq := &Squad{
		ID:        id,
		Level:     level,
		Position:  [3]float32{x, y, z},
		State:     AIStatePatrol,
		Path:      path,
		PathIndex: 0,
		Health:    100.0,
		Faction:   faction,
		SpawnedAt: time.Now(),
	}

	sm.squads[id] = sq
	return sq
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

// Tick advances all squad AI states and movement, then calls broadcastFn for
// every live squad so the caller can fan out packets to nearby sessions.
// broadcastFn receives (squadID, currentState, currentPosition).
//
// Tick deliberately does not import internal/game to avoid circular imports.
func (sm *SquadManager) Tick(_ time.Time, broadcastFn func(squadID uint32, state AIState, pos [3]float32)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, sq := range sm.squads {
		if sq.State == AIStateDead {
			continue
		}

		if sq.State == AIStatePatrol {
			sq.advancePatrol()
		}

		broadcastFn(sq.ID, sq.State, sq.Position)
	}
}

// Count returns the number of active (non-removed) squads.
func (sm *SquadManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.squads)
}

// advancePatrol moves the squad one step along its path, looping when the end
// is reached so the squad continuously patrols the circuit.
func (sq *Squad) advancePatrol() {
	if len(sq.Path) == 0 {
		return
	}

	target := sq.Path[sq.PathIndex]
	dx := target.X - sq.Position[0]
	dy := target.Y - sq.Position[1]
	dz := target.Z - sq.Position[2]

	// Euclidean distance to the next waypoint.
	distSq := dx*dx + dy*dy + dz*dz
	if distSq <= patrolStepSize*patrolStepSize {
		// Snap to waypoint and advance the index (loop).
		sq.Position = [3]float32{target.X, target.Y, target.Z}
		sq.PathIndex = (sq.PathIndex + 1) % len(sq.Path)
		return
	}

	// Move patrolStepSize units toward the target.
	var dist float32
	for v := distSq; ; {
		dist = v
		v = (v + distSq/v) / 2
		if v >= dist {
			break
		}
		dist = v
	}

	if dist > 0 {
		sq.Position[0] += (dx / dist) * patrolStepSize
		sq.Position[1] += (dy / dist) * patrolStepSize
		sq.Position[2] += (dz / dist) * patrolStepSize
	}
}

// ---------------------------------------------------------------------------
// patrolGrid — minimal Graph implementation for A* patrol path generation.
// It builds a flat 2-D walkable grid between two world-space waypoints so the
// pathfinder has a concrete graph to traverse without importing engine data.
// ---------------------------------------------------------------------------

// patrolGrid implements the Graph interface used by AStar.
type patrolGrid struct {
	nodes map[Waypoint][]Waypoint
}

// newPatrolGrid builds a sparse grid of waypoints connecting start and goal by
// stepping in world-unit increments along X and Z.
func newPatrolGrid(start, goal Waypoint) *patrolGrid {
	g := &patrolGrid{nodes: make(map[Waypoint][]Waypoint)}

	// Generate a chain of waypoints from start to goal at fixed step intervals.
	step := patrolStepSize * 4 // coarse grid — fine enough for pathing
	if step <= 0 {
		step = 8.0
	}

	// Build a simple grid row connecting start.X→goal.X at start.Z, then a
	// column from start.Z→goal.Z at goal.X, forming an L-shaped patrol route.
	var chain []Waypoint
	chain = append(chain, start)

	// Horizontal leg
	x := start.X + step
	for x < goal.X {
		chain = append(chain, Waypoint{X: x, Y: start.Y, Z: start.Z})
		x += step
	}

	// Corner
	corner := Waypoint{X: goal.X, Y: start.Y, Z: start.Z}
	chain = append(chain, corner)

	// Vertical leg
	z := start.Z + step
	for z < goal.Z {
		chain = append(chain, Waypoint{X: goal.X, Y: start.Y, Z: z})
		z += step
	}

	chain = append(chain, goal)

	// Wire neighbours (bidirectional chain).
	for i, wp := range chain {
		var nbrs []Waypoint
		if i > 0 {
			nbrs = append(nbrs, chain[i-1])
		}
		if i < len(chain)-1 {
			nbrs = append(nbrs, chain[i+1])
		}
		g.nodes[wp] = nbrs
	}

	return g
}

func (g *patrolGrid) GetNeighbors(node Waypoint) []Waypoint {
	return g.nodes[node]
}
