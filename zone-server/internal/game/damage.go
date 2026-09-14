package game

import (
	"math"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"

	"go.uber.org/zap"
)

// DamageHandler validates and applies combat damage between players.
type DamageHandler struct {
	logger          *zap.Logger
	maxRange        float32 // e.g. 300.0 meters
	maxDamagePerHit float32 // e.g. 150.0
}

// NewDamageHandler creates a new initialized DamageHandler.
func NewDamageHandler(logger ...*zap.Logger) *DamageHandler {
	dh := &DamageHandler{
		maxRange:        300.0,
		maxDamagePerHit: 150.0,
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
	attackerInSafe := attacker.InSafeZone
	attackerPos := attacker.Position
	attacker.Unlock()

	target.Lock()
	targetID := target.SessionID
	targetInSafe := target.InSafeZone
	targetPos := target.Position
	target.Unlock()

	if dmg.AttackerID != attackerID || dmg.TargetID != targetID {
		return 0, false, "session mismatch"
	}

	// 3. Safe zone immunity (Issue 15):
	// Attacker and target lock checked: if attacker.InSafeZone or target.InSafeZone, return (0, false, "safe zone immunity").
	if attackerInSafe || targetInSafe {
		return 0, false, "safe zone immunity"
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
