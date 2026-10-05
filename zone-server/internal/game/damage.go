package game

import (
	"math"
	"sync"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"

	"go.uber.org/zap"
)

// damageSpend is one accepted hit inside the rolling damage budget window.
type damageSpend struct {
	at     time.Time
	amount float64
}

// DamageHandler validates and applies combat damage between players.
type DamageHandler struct {
	logger          *zap.Logger
	maxRange        float32 // e.g. 300.0 meters
	maxDamagePerHit float32 // e.g. 150.0
	groups          *GroupManager

	// Rolling per-attacker damage budget. Guarded by budgetMu because UDP
	// workers handle damage concurrently.
	budgetMu         sync.Mutex
	damageBudgetPerS float64
	budget           map[uint32][]damageSpend

	now func() time.Time
}

// SetGroupManager wires party membership into the friendly-fire gate. A nil
// manager disables the same-group check (used by unit tests).
func (dh *DamageHandler) SetGroupManager(groups *GroupManager) {
	dh.groups = groups
}

// SetDamageBudgetPerS overrides the rolling 1 s damage budget. Values <= 0
// disable the budget entirely.
func (dh *DamageHandler) SetDamageBudgetPerS(v float64) {
	dh.damageBudgetPerS = v
}

// SetNowFunc overrides the clock (tests only).
func (dh *DamageHandler) SetNowFunc(f func() time.Time) {
	if f != nil {
		dh.now = f
	}
}

// ClearBudget drops the rolling damage budget for a departed session so the
// map cannot grow with disconnected players.
func (dh *DamageHandler) ClearBudget(sessionID uint32) {
	dh.budgetMu.Lock()
	delete(dh.budget, sessionID)
	dh.budgetMu.Unlock()
}

// NewDamageHandler creates a new initialized DamageHandler.
func NewDamageHandler(logger ...*zap.Logger) *DamageHandler {
	dh := &DamageHandler{
		maxRange:         300.0,
		maxDamagePerHit:  150.0,
		damageBudgetPerS: 400.0,
		budget:           make(map[uint32][]damageSpend),
		now:              time.Now,
	}
	if len(logger) > 0 && logger[0] != nil {
		dh.logger = logger[0]
	}
	return dh
}

// SetMaxRange configures the maximum allowed weapon range.
func (dh *DamageHandler) SetMaxRange(r float32) {
	dh.maxRange = r
}

// SetMaxDamagePerHit configures the maximum damage clamped per hit.
func (dh *DamageHandler) SetMaxDamagePerHit(d float32) {
	dh.maxDamagePerHit = d
}

// ValidateAndApplyDamage verifies rules 1-7 and applies damage to the target.
func (dh *DamageHandler) ValidateAndApplyDamage(
	attacker *network.PlayerSession,
	target *network.PlayerSession,
	dmg *protocol.DamageNotify,
) (appliedDamage float32, valid bool, reason string) {
	// 1. Nil checks: attacker and target must not be nil.
	if attacker == nil || target == nil {
		return 0, false, "nil session"
	}
	if dmg == nil {
		return 0, false, "nil damage payload"
	}

	// 2. Session match: dmg.AttackerID == attacker.SessionID and dmg.TargetID == target.SessionID.
	attacker.Lock()
	attackerID := attacker.SessionID
	attackerPos := attacker.Position
	attackerLevel := attacker.CurrentLevel
	attackerInSafe := attacker.InSafeZone
	attackerFaction := attacker.Faction
	attacker.Unlock()

	target.Lock()
	targetID := target.SessionID
	targetInSafe := target.InSafeZone
	targetPos := target.Position
	targetLevel := target.CurrentLevel
	targetFaction := target.Faction
	target.Unlock()

	// PRODUCTION FIX: cross-map hits (Cordon -> Rostok) were possible because
	// only 3D distance was checked. Levels are separate instances; damage
	// across levels is always forged.
	if attackerLevel != targetLevel {
		return 0, false, "cross-level damage rejected"
	}

	if dmg.AttackerID != attackerID || dmg.TargetID != targetID {
		return 0, false, "session mismatch"
	}

	// 2b. Self-damage is never valid; reject before any friendly-fire gate so
	// a forged packet cannot be laundered as a no-op friendly hit.
	if attackerID == targetID {
		return 0, false, "self damage rejected"
	}

	// 3. Safe zone immunity (both sides):
	// A player inside a safe zone may neither deal nor receive damage there.
	// Return (0, false, "safe zone immunity") when either side is protected.
	if attackerInSafe || targetInSafe {
		return 0, false, "safe zone immunity"
	}

	// 3b. Friendly-fire gate: party members never damage each other, and
	// players of the same normalized faction (actor_ prefix optional) never
	// damage each other. Different factions and unset factions fall through.
	if dh.groups != nil && dh.groups.IsSameGroup(attackerID, targetID) {
		return 0, false, "same group friendly fire"
	}
	if sameFaction(attackerFaction, targetFaction) {
		return 0, false, "same faction friendly fire"
	}

	// 4. Distance check (Issue 18):
	// Calculate Euclidean distance between attacker position and target position. If distance > dh.maxRange (300.0m), return (0, false, "attack out of range").
	maxRange := dh.maxRange
	if maxRange <= 0 {
		maxRange = 300.0
	}
	dx := float64(attackerPos[0] - targetPos[0])
	dy := float64(attackerPos[1] - targetPos[1])
	dz := float64(attackerPos[2] - targetPos[2])
	dist := float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
	if dist > maxRange {
		return 0, false, "attack out of range"
	}

	// 5. Damage sanity:
	// If dmg.Damage <= 0 || math.IsNaN(float64(dmg.Damage)) || math.IsInf(float64(dmg.Damage), 0): return (0, false, "invalid damage value").
	// If dmg.Damage > 250.0: return (0, false, "damage exceeds sanity ceiling").
	// Clamp damage to dh.maxDamagePerHit.
	dmgVal := float64(dmg.Damage)
	if dmg.Damage <= 0 || math.IsNaN(dmgVal) || math.IsInf(dmgVal, 0) {
		return 0, false, "invalid damage value"
	}
	if dmg.Damage > 250.0 {
		return 0, false, "damage exceeds sanity ceiling"
	}

	maxDmg := dh.maxDamagePerHit
	if maxDmg <= 0 {
		maxDmg = 150.0
	}
	appliedDamage = dmg.Damage
	if appliedDamage > maxDmg {
		appliedDamage = maxDmg
	}

	// 5b. Rolling damage budget: clamp the hit to what remains of the last
	// second's budget and reject once exhausted. Generous by default (400/s)
	// so sustained melee/automatic fire stays viable.
	if dh.damageBudgetPerS > 0 {
		charged, ok := dh.chargeBudget(attackerID, float64(appliedDamage))
		if !ok || charged <= 0 {
			return 0, false, "damage budget exceeded"
		}
		appliedDamage = float32(charged)
	}

	// 6. Combat status tracking:
	// Set combat status on both attacker and target for 30 seconds (attacker.SetCombat(30 * time.Second), target.SetCombat(30 * time.Second)).
	attacker.SetCombat(30 * time.Second)
	target.SetCombat(30 * time.Second)

	// 7. Apply damage:
	// target.Lock(), decrement target.Health -= appliedDamage. If target.Health < 0, clamp to 0. target.Dirty = true. target.Unlock().
	// Return (appliedDamage, true, "").
	target.Lock()
	target.Health -= appliedDamage
	if target.Health < 0 {
		target.Health = 0
	}
	target.Dirty = true
	target.Unlock()

	if dh.logger != nil {
		dh.logger.Debug("damage applied",
			zap.Uint32("attacker", attackerID),
			zap.Uint32("target", targetID),
			zap.Float32("damage", appliedDamage),
		)
	}

	return appliedDamage, true, ""
}

// chargeBudget prunes the attacker's rolling 1 s window, clamps amount to the
// remaining budget and records the accepted spend. It returns ok=false when
// the budget is already exhausted. The single-hit cap is applied by the
// caller before this function.
func (dh *DamageHandler) chargeBudget(attackerID uint32, amount float64) (float64, bool) {
	dh.budgetMu.Lock()
	defer dh.budgetMu.Unlock()

	now := time.Now()
	if dh.now != nil {
		now = dh.now()
	}
	cutoff := now.Add(-time.Second)

	kept := dh.budget[attackerID][:0]
	spent := 0.0
	for _, e := range dh.budget[attackerID] {
		if e.at.After(cutoff) {
			kept = append(kept, e)
			spent += e.amount
		}
	}

	remaining := dh.damageBudgetPerS - spent
	if remaining <= 0 {
		dh.budget[attackerID] = kept
		return 0, false
	}
	if amount > remaining {
		amount = remaining
	}
	kept = append(kept, damageSpend{at: now, amount: amount})
	dh.budget[attackerID] = kept
	return amount, true
}
