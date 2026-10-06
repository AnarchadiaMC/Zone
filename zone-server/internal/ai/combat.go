package ai

import (
	"math"
	"time"
)

// CombatParams holds the resolved combat FSM knobs. Zero values are replaced by
// the package defaults so callers can leave fields unset in tests.
type CombatParams struct {
	AggroRadius    float32
	AttackRange    float32
	AttackCooldown time.Duration
	MeleeDamage    float32
	PatrolResume   time.Duration
}

// Combat default values; the game package resolves config keys onto these.
const (
	DefaultAggroRadius    float32       = 40.0
	DefaultAttackRange    float32       = 2.0
	DefaultAttackCooldown time.Duration = 1500 * time.Millisecond
	DefaultMeleeDamage    float32       = 10.0
	DefaultPatrolResume   time.Duration = 10 * time.Second
)

// withDefaults fills unset fields with the package defaults.
func (p CombatParams) withDefaults() CombatParams {
	if p.AggroRadius <= 0 {
		p.AggroRadius = DefaultAggroRadius
	}
	if p.AttackRange <= 0 {
		p.AttackRange = DefaultAttackRange
	}
	if p.AttackCooldown <= 0 {
		p.AttackCooldown = DefaultAttackCooldown
	}
	if p.MeleeDamage <= 0 {
		p.MeleeDamage = DefaultMeleeDamage
	}
	if p.PatrolResume <= 0 {
		p.PatrolResume = DefaultPatrolResume
	}
	return p
}

// CombatTarget is one candidate player for the combat FSM. The game package
// filters the list to alive, same-level players outside safe zones; the FSM
// still applies the hostility callback before engaging.
type CombatTarget struct {
	ID       uint32
	Level    string
	Position [3]float32
	Faction  string
}

// CombatStep carries the per-tick combat inputs. Enabled=false keeps the
// patrol-only simulation. Hostile is the faction relation gate; SquadGate, when
// non-nil, can veto engagement for a squad (used for safe-zone checks).
type CombatStep struct {
	Enabled   bool
	Params    CombatParams
	Hostile   func(squadFaction, targetFaction string) bool
	SquadGate func(sq *Squad) bool
	Targets   []CombatTarget
}

// AIAttackEvent is one melee swing produced by the FSM. The caller validates it
// through the shared damage pipeline and relays it to the victim; carrying the
// squad's position/level/faction lets validation run after the manager lock is
// released without re-reading the squad.
type AIAttackEvent struct {
	SquadID  uint32
	TargetID uint32
	Damage   float32
	Position [3]float32
	Level    string
	Faction  string
}

// stepCombat advances the combat FSM for one puppet. It returns true when it
// owns the squad's movement/animation for this step (Alert/Chase/Attack), false
// when the caller should run the normal patrol step (Idle/Patrol). Caller holds
// sm.mu; the death branch is handled by the caller before this is invoked.
func stepCombat(sq *Squad, now time.Time, step CombatStep, dt float64, attacks *[]AIAttackEvent) bool {
	params := step.Params.withDefaults()

	if step.SquadGate != nil && !step.SquadGate(sq) {
		// Cannot fight from here (e.g. standing in a safe zone): drop any
		// target and resume patrol.
		sq.TargetID = 0
		sq.State = AIStatePatrol
		return false
	}

	target := findTarget(step.Targets, sq.Level, sq.TargetID)
	hostile := func(t *CombatTarget) bool {
		if t == nil {
			return false
		}
		if step.Hostile == nil {
			return true
		}
		return step.Hostile(sq.Faction, t.Faction)
	}

	switch sq.State {
	case AIStateIdle, AIStatePatrol:
		if best := nearestHostile(sq, step.Targets, params.AggroRadius, step.Hostile); best != nil {
			sq.TargetID = best.ID
			sq.State = AIStateChase
			// Fall through to the chase evaluation below on the same tick so
			// a target already inside attack range starts swinging immediately.
			return stepChase(sq, now, params, targetFor(step.Targets, sq.Level, sq.TargetID), dt, attacks)
		}
		return false

	case AIStateAlert:
		if target != nil && hostile(target) && distSq3D(sq.Position, target.Position) <= params.AggroRadius*params.AggroRadius {
			sq.TargetID = target.ID
			sq.State = AIStateChase
			return stepChase(sq, now, params, target, dt, attacks)
		}
		if !sq.AlertUntil.IsZero() && !now.Before(sq.AlertUntil) {
			sq.State = AIStatePatrol
			sq.TargetID = 0
			sq.AlertUntil = time.Time{}
		}
		sq.Anim = AnimIdle
		return true

	case AIStateChase:
		if !hostile(target) {
			return stepAlert(sq, now, params)
		}
		return stepChase(sq, now, params, target, dt, attacks)

	case AIStateAttack:
		if !hostile(target) {
			return stepAlert(sq, now, params)
		}
		if distSq3D(sq.Position, target.Position) > params.AttackRange*params.AttackRange {
			sq.State = AIStateChase
			return stepChase(sq, now, params, target, dt, attacks)
		}
		faceTarget(sq, target.Position)
		sq.Anim = AnimAttack
		if sq.LastAttack.IsZero() || now.Sub(sq.LastAttack) >= params.AttackCooldown {
			sq.LastAttack = now
			*attacks = append(*attacks, AIAttackEvent{
				SquadID:  sq.ID,
				TargetID: target.ID,
				Damage:   params.MeleeDamage,
				Position: sq.Position,
				Level:    sq.Level,
				Faction:  sq.Faction,
			})
		}
		return true
	}
	return false
}

// stepChase moves the squad toward the target at run speed and transitions to
// Attack inside range. A nil/forbidden target drops to Alert.
func stepChase(sq *Squad, now time.Time, params CombatParams, target *CombatTarget, dt float64, attacks *[]AIAttackEvent) bool {
	if target == nil {
		return stepAlert(sq, now, params)
	}
	dx := target.Position[0] - sq.Position[0]
	dy := target.Position[1] - sq.Position[1]
	dz := target.Position[2] - sq.Position[2]
	distSq := dx*dx + dy*dy + dz*dz
	if distSq <= params.AttackRange*params.AttackRange {
		sq.State = AIStateAttack
		faceTarget(sq, target.Position)
		sq.Anim = AnimAttack
		if sq.LastAttack.IsZero() || now.Sub(sq.LastAttack) >= params.AttackCooldown {
			sq.LastAttack = now
			*attacks = append(*attacks, AIAttackEvent{
				SquadID:  sq.ID,
				TargetID: target.ID,
				Damage:   params.MeleeDamage,
				Position: sq.Position,
				Level:    sq.Level,
				Faction:  sq.Faction,
			})
		}
		return true
	}

	faceTarget(sq, target.Position)
	sq.Anim = AnimRun
	speed := sq.RunSpeed
	if speed <= 0 {
		speed = defaultRunSpeed
	}
	if dt > 0 {
		dist := float32(math.Sqrt(float64(distSq)))
		stepLen := float32(float64(speed) * dt)
		if stepLen > dist {
			stepLen = dist
		}
		sq.Position[0] += dx / dist * stepLen
		sq.Position[1] += dy / dist * stepLen
		sq.Position[2] += dz / dist * stepLen
	}
	return true
}

// stepAlert enters the Alert cool-down before the squad resumes patrol.
func stepAlert(sq *Squad, now time.Time, params CombatParams) bool {
	sq.TargetID = 0
	sq.State = AIStateAlert
	sq.AlertUntil = now.Add(params.PatrolResume)
	sq.Anim = AnimIdle
	return true
}

// nearestHostile returns the closest hostile target inside the aggro radius.
func nearestHostile(sq *Squad, targets []CombatTarget, radius float32, hostile func(string, string) bool) *CombatTarget {
	radiusSq := radius * radius
	var best *CombatTarget
	var bestDist float32
	for i := range targets {
		t := &targets[i]
		if t.Level != sq.Level {
			continue
		}
		if hostile != nil && !hostile(sq.Faction, t.Faction) {
			continue
		}
		d := distSq3D(sq.Position, t.Position)
		if d > radiusSq {
			continue
		}
		if best == nil || d < bestDist {
			best = t
			bestDist = d
		}
	}
	return best
}

// findTarget resolves the squad's current target on its level, or nil.
func findTarget(targets []CombatTarget, level string, id uint32) *CombatTarget {
	if id == 0 {
		return nil
	}
	return targetFor(targets, level, id)
}

func targetFor(targets []CombatTarget, level string, id uint32) *CombatTarget {
	for i := range targets {
		if targets[i].ID == id && targets[i].Level == level {
			return &targets[i]
		}
	}
	return nil
}

// faceTarget yaws the squad toward a point.
func faceTarget(sq *Squad, pos [3]float32) {
	dx := pos[0] - sq.Position[0]
	dz := pos[2] - sq.Position[2]
	if dx == 0 && dz == 0 {
		return
	}
	sq.Yaw = float32(math.Atan2(float64(dx), float64(dz)))
}

func distSq3D(a, b [3]float32) float32 {
	dx := a[0] - b[0]
	dy := a[1] - b[1]
	dz := a[2] - b[2]
	return dx*dx + dy*dy + dz*dz
}

