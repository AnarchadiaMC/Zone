package ai

import (
	"math"
	"time"
)

// Animation wire values carried by OpAIState entries. They mirror the frozen
// protocol (0=idle, 1=walk, 2=run, 3=attack, 4=death); attack and death are
// defined for completeness but unused in the no-combat puppet wave.
const (
	AnimIdle   uint8 = 0
	AnimWalk   uint8 = 1
	AnimRun    uint8 = 2
	AnimAttack uint8 = 3
	AnimDeath  uint8 = 4
)

const (
	defaultPatrolRadius float32 = 32.0
	minPatrolRadius     float32 = 5.0
	maxPatrolRadius     float32 = 200.0
	defaultWalkSpeed    float32 = 1.5
	defaultRunSpeed     float32 = 3.0
	puppetLoopPoints            = 12
	onlineStepClampSec          = 0.25
	offlineStepInterval         = time.Second
	offlineStepClampSec         = 2.0
	// puppetDespawnDelay is how long a dead puppet keeps broadcasting its
	// death animation before the squad is despawned and its entity released.
	puppetDespawnDelay = 5 * time.Second
)

// PuppetDef describes one server-replicated AI puppet squad loaded from the
// ai_squads table (or registered directly by tests).
type PuppetDef struct {
	DBID         int64
	Label        string
	Section      string
	Faction      string
	Level        string
	Spawn        [3]float32
	PatrolRadius float32
	WalkSpeed    float32
	RunSpeed     float32
	// Loop optionally overrides the generated circular patrol circuit. Each
	// waypoint must already be in world space.
	Loop []Waypoint
}

// PuppetState is an immutable snapshot of one puppet squad, copied under the
// manager lock so callers (game tick, tests) can read it without data races.
type PuppetState struct {
	ID       uint32
	Label    string
	Section  string
	Faction  string
	Level    string
	Position [3]float32
	Yaw      float32
	Anim     uint8
	Online   bool
	Health   float32
}

// RegisterPuppet creates a replicable puppet squad with a generated loop when
// the definition does not supply waypoints. It returns the new squad.
func (sm *SquadManager) RegisterPuppet(def PuppetDef) *Squad {
	radius := def.PatrolRadius
	if radius <= 0 {
		radius = defaultPatrolRadius
	}
	radius = clampFloat32(radius, minPatrolRadius, maxPatrolRadius)

	walk := def.WalkSpeed
	if walk <= 0 {
		walk = defaultWalkSpeed
	}
	run := def.RunSpeed
	if run <= 0 {
		run = defaultRunSpeed
	}

	loop := def.Loop
	if len(loop) < 2 {
		loop = circleLoop(def.Spawn, radius, puppetLoopPoints)
	} else {
		loop = append([]Waypoint(nil), loop...)
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	id := sm.nextID
	sm.nextID++

	sq := &Squad{
		ID:           id,
		DBID:         def.DBID,
		Level:        def.Level,
		Position:     def.Spawn,
		State:        AIStatePatrol,
		Health:       100.0,
		Faction:      def.Faction,
		Puppet:       true,
		Label:        def.Label,
		Section:      def.Section,
		PatrolRadius: radius,
		WalkSpeed:    walk,
		RunSpeed:     run,
		Anim:         AnimIdle,
		Loop:         loop,
	}
	sm.squads[id] = sq
	return sq
}

// TickPuppets advances every puppet squad one simulation step. Squads whose ID
// is present in online step at the caller's tick rate with elapsed time (the
// game tick); all other squads use a 1 Hz macro-step so off-screen patrols keep
// moving cheaply. Dead squads are still processed: they broadcast AnimDeath and
// are despawned (returned in the result) after puppetDespawnDelay, so the
// caller can release their replication entity instead of leaking a corpse
// forever. Must be called from the single game-loop goroutine.
func (sm *SquadManager) TickPuppets(now time.Time, online map[uint32]bool) []uint32 {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var despawned []uint32
	for id, sq := range sm.squads {
		if !sq.Puppet {
			continue
		}

		isOnline := online[sq.ID]
		sq.Online = isOnline

		if sq.State == AIStateDead {
			// Death branch runs BEFORE any skip: the death animation must be
			// visible, and the squad must eventually be released.
			if sq.deadSince.IsZero() {
				sq.deadSince = now
			}
			applyPuppetStep(sq, now, 0)
			if now.Sub(sq.deadSince) >= puppetDespawnDelay {
				delete(sm.squads, id)
				despawned = append(despawned, id)
			}
			continue
		}

		if isOnline {
			dt := 0.0
			if !sq.lastStep.IsZero() {
				dt = now.Sub(sq.lastStep).Seconds()
				if dt < 0 {
					dt = 0
				} else if dt > onlineStepClampSec {
					// Clamp: a paused loop resumes with at most a 250 ms step
					// instead of teleporting the puppet by the full gap.
					dt = onlineStepClampSec
				}
			}
			sq.lastStep = now
			applyPuppetStep(sq, now, dt)
			continue
		}

		// Offline tier: macro-step at 1 Hz. lastStep advances only when a step
		// actually happens so a pause does not accumulate unbounded catch-up.
		if sq.lastStep.IsZero() {
			sq.lastStep = now
			applyPuppetStep(sq, now, 0)
			continue
		}
		elapsed := now.Sub(sq.lastStep).Seconds()
		if elapsed < offlineStepInterval.Seconds() {
			applyPuppetStep(sq, now, 0)
			continue
		}
		if elapsed > offlineStepClampSec {
			// Clamp: at most a 2 s macro-step after a long stall.
			elapsed = offlineStepClampSec
		}
		sq.lastStep = now
		applyPuppetStep(sq, now, elapsed)
	}
	return despawned
}

// applyPuppetStep moves sq along its patrol loop by speed*dt seconds and
// refreshes Yaw/Anim. dt <= 0 only refreshes the stationary animation. The
// walk/run animation is derived directly from whether the runUntil window is
// active, so Anim always matches the chosen speed.
func applyPuppetStep(sq *Squad, now time.Time, dt float64) {
	if len(sq.Loop) == 0 {
		sq.Anim = AnimIdle
		return
	}
	if sq.State == AIStateDead {
		sq.Anim = AnimDeath
		return
	}
	if dt <= 0 {
		sq.Anim = AnimIdle
		return
	}

	usingRun := !sq.runUntil.IsZero() && sq.runUntil.After(now) && sq.RunSpeed > 0
	speed := sq.WalkSpeed
	if usingRun {
		speed = sq.RunSpeed
	}
	if speed <= 0 {
		speed = defaultWalkSpeed
	}

	if sq.LoopIndex < 0 || sq.LoopIndex >= len(sq.Loop) {
		sq.LoopIndex = 0
	}
	target := sq.Loop[sq.LoopIndex]
	dx := target.X - sq.Position[0]
	dy := target.Y - sq.Position[1]
	dz := target.Z - sq.Position[2]
	distSq := dx*dx + dy*dy + dz*dz

	if distSq < 1e-6 {
		sq.LoopIndex = (sq.LoopIndex + 1) % len(sq.Loop)
		sq.Anim = AnimIdle
		return
	}

	step := float32(float64(speed) * dt)
	dist := float32(math.Sqrt(float64(distSq)))
	if dist <= step {
		sq.Position = [3]float32{target.X, target.Y, target.Z}
		sq.LoopIndex = (sq.LoopIndex + 1) % len(sq.Loop)
	} else {
		sq.Position[0] += dx / dist * step
		sq.Position[1] += dy / dist * step
		sq.Position[2] += dz / dist * step
	}
	sq.Yaw = float32(math.Atan2(float64(dx), float64(dz)))
	if usingRun {
		sq.Anim = AnimRun
	} else {
		sq.Anim = AnimWalk
	}
}

// SnapshotPuppets returns value copies of all puppet squads (including dead
// ones, so callers can see the death animation).
func (sm *SquadManager) SnapshotPuppets() []PuppetState {
	return sm.SnapshotPuppetsInto(nil)
}

// SnapshotPuppetsInto is SnapshotPuppets with a caller-provided reusable
// buffer, so the per-tick replication path performs no slice allocation after
// warmup. The returned slice aliases buf.
func (sm *SquadManager) SnapshotPuppetsInto(buf []PuppetState) []PuppetState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	buf = buf[:0]
	for _, sq := range sm.squads {
		if !sq.Puppet {
			continue
		}
		buf = append(buf, PuppetState{
			ID:       sq.ID,
			Label:    sq.Label,
			Section:  sq.Section,
			Faction:  sq.Faction,
			Level:    sq.Level,
			Position: sq.Position,
			Yaw:      sq.Yaw,
			Anim:     sq.Anim,
			Online:   sq.Online,
			Health:   sq.Health,
		})
	}
	return buf
}

// PuppetCount returns the number of registered puppet squads.
func (sm *SquadManager) PuppetCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	n := 0
	for _, sq := range sm.squads {
		if sq.Puppet {
			n++
		}
	}
	return n
}

// SetPuppetRun makes the squad move at RunSpeed until until, with the wire
// animation derived from the same window (AnimRun while active, AnimWalk
// otherwise). Not scheduled by default in wave A; exposed for future combat
// hooks.
func (sm *SquadManager) SetPuppetRun(id uint32, until time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sq, ok := sm.squads[id]; ok && sq.Puppet {
		sq.runUntil = until
	}
}

// SetPuppetPosition teleports a puppet (tests, future teleport hooks).
func (sm *SquadManager) SetPuppetPosition(id uint32, x, y, z float32) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sq, ok := sm.squads[id]
	if !ok || !sq.Puppet {
		return false
	}
	sq.Position = [3]float32{x, y, z}
	return true
}

// circleLoop generates a rounded patrol circuit around spawn. Points start on
// the +X axis and advance counter-clockwise in the XZ plane at spawn.Y.
func circleLoop(spawn [3]float32, radius float32, points int) []Waypoint {
	if points < 3 {
		points = 3
	}
	loop := make([]Waypoint, 0, points)
	for i := 0; i < points; i++ {
		angle := 2 * math.Pi * float64(i) / float64(points)
		loop = append(loop, Waypoint{
			X: spawn[0] + radius*float32(math.Cos(angle)),
			Y: spawn[1],
			Z: spawn[2] + radius*float32(math.Sin(angle)),
		})
	}
	return loop
}

func clampFloat32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
