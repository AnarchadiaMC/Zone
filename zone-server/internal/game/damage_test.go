package game

import (
	"math"
	"testing"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

func createTestSessions() (*network.PlayerSession, *network.PlayerSession) {
	attacker := &network.PlayerSession{
		SessionID: 1,
		AccountID: "attacker-uuid",
		Health:    100.0,
		Position:  [3]float32{0, 0, 0},
	}
	target := &network.PlayerSession{
		SessionID: 2,
		AccountID: "target-uuid",
		Health:    100.0,
		Position:  [3]float32{10, 0, 0},
	}
	return attacker, target
}

func TestDamage_NilChecks(t *testing.T) {
	dh := NewDamageHandler()
	attacker, target := createTestSessions()
	dmg := &protocol.DamageNotify{
		AttackerID: 1,
		TargetID:   2,
		Damage:     50.0,
	}

	// 1. Nil attacker
	applied, valid, reason := dh.ValidateAndApplyDamage(nil, target, dmg)
	if valid || applied != 0 || reason != "nil session" {
		t.Fatalf("expected nil session for nil attacker, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 2. Nil target
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, nil, dmg)
	if valid || applied != 0 || reason != "nil session" {
		t.Fatalf("expected nil session for nil target, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 3. Nil dmg
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, nil)
	if valid || applied != 0 || reason != "nil damage payload" {
		t.Fatalf("expected nil damage payload for nil dmg, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}
}

func TestDamage_SessionMismatch(t *testing.T) {
	dh := NewDamageHandler()
	attacker, target := createTestSessions()

	// Attacker mismatch
	dmg := &protocol.DamageNotify{
		AttackerID: 999,
		TargetID:   2,
		Damage:     50.0,
	}
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "session mismatch" {
		t.Fatalf("expected session mismatch for attacker, got valid=%v, reason=%q", valid, reason)
	}

	// Target mismatch
	dmg = &protocol.DamageNotify{
		AttackerID: 1,
		TargetID:   888,
		Damage:     50.0,
	}
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "session mismatch" {
		t.Fatalf("expected session mismatch for target, got valid=%v, reason=%q", valid, reason)
	}
}

func TestDamage_SafeZoneImmunity(t *testing.T) {
	dh := NewDamageHandler()

	// Case 1: Attacker in safe zone, target outside -> allowed
	attacker, target := createTestSessions()
	attacker.InSafeZone = true
	dmg := &protocol.DamageNotify{AttackerID: 1, TargetID: 2, Damage: 50.0}
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 50.0 || reason != "" {
		t.Fatalf("expected damage allowed when only attacker in safe zone, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}
	if target.Health != 50.0 {
		t.Fatalf("target health should decrease to 50, got %f", target.Health)
	}

	// Case 2: Attacker outside, target in safe zone
	attacker, target = createTestSessions()
	target.InSafeZone = true
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "safe zone immunity" {
		t.Fatalf("expected safe zone immunity when target is in safe zone, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}
	if target.Health != 100.0 {
		t.Fatalf("target health should not decrease, got %f", target.Health)
	}

	// Case 3: Both in safe zone
	attacker, target = createTestSessions()
	attacker.InSafeZone = true
	target.InSafeZone = true
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "safe zone immunity" {
		t.Fatalf("expected safe zone immunity when both in safe zone, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// Case 4: Neither in safe zone -> damage applied
	attacker, target = createTestSessions()
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 50.0 || reason != "" {
		t.Fatalf("expected damage to succeed when outside safe zone, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}
	if target.Health != 50.0 {
		t.Fatalf("expected target health to be 50.0, got %f", target.Health)
	}
}

func TestDamage_AttackDistanceValidation(t *testing.T) {
	dh := NewDamageHandler() // default maxRange = 300.0m
	attacker, target := createTestSessions()

	// 1. Within range: 250m
	attacker.Position = [3]float32{0, 0, 0}
	target.Position = [3]float32{250, 0, 0}
	dmg := &protocol.DamageNotify{AttackerID: 1, TargetID: 2, Damage: 30.0}
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 30.0 {
		t.Fatalf("expected attack within 250m to be valid, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 2. Exactly at range edge: 300m
	target.Position = [3]float32{300, 0, 0}
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 30.0 {
		t.Fatalf("expected attack at exactly 300m to be valid, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 3. Out of range: 300.1m
	target.Position = [3]float32{300.1, 0, 0}
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "attack out of range" {
		t.Fatalf("expected attack beyond 300m to fail, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 4. Out of range 3D: (200, 200, 200) -> dist = sqrt(120000) ~ 346.4m > 300m
	target.Position = [3]float32{200, 200, 200}
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "attack out of range" {
		t.Fatalf("expected 3D out of range attack to fail, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}
}

func TestDamage_SanityAndClamping(t *testing.T) {
	dh := NewDamageHandler() // default maxDamagePerHit = 150.0
	attacker, target := createTestSessions()

	// 1. Zero damage
	dmg := &protocol.DamageNotify{AttackerID: 1, TargetID: 2, Damage: 0.0}
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "invalid damage value" {
		t.Fatalf("expected 0 damage to be invalid, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 2. Negative damage
	dmg.Damage = -15.0
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "invalid damage value" {
		t.Fatalf("expected negative damage to be invalid, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 3. NaN damage
	dmg.Damage = float32(math.NaN())
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "invalid damage value" {
		t.Fatalf("expected NaN damage to be invalid, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 4. Inf damage
	dmg.Damage = float32(math.Inf(1))
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "invalid damage value" {
		t.Fatalf("expected Inf damage to be invalid, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 5. Exceeding sanity ceiling (> 250.0)
	dmg.Damage = 250.1
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "damage exceeds sanity ceiling" {
		t.Fatalf("expected damage > 250 to be rejected, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	dmg.Damage = 1000.0
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 || reason != "damage exceeds sanity ceiling" {
		t.Fatalf("expected damage=1000 to be rejected, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// 6. Clamping between maxDamagePerHit (150.0) and ceiling (250.0):
	// E.g. 200 damage -> clamped to 150.0
	attacker, target = createTestSessions()
	target.Health = 200.0
	dmg.Damage = 200.0
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 150.0 || reason != "" {
		t.Fatalf("expected damage 200 to be clamped to 150.0, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}
	if target.Health != 50.0 {
		t.Fatalf("expected target health 200 - 150 = 50.0, got %f", target.Health)
	}

	// 7. Normal damage under maxDamagePerHit: e.g. 75.0
	attacker, target = createTestSessions()
	dmg.Damage = 75.0
	applied, valid, reason = dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 75.0 || reason != "" {
		t.Fatalf("expected damage 75 to be applied directly, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}
	if target.Health != 25.0 {
		t.Fatalf("expected target health 100 - 75 = 25.0, got %f", target.Health)
	}
}

func TestDamage_CombatStateTracking(t *testing.T) {
	dh := NewDamageHandler()
	attacker, target := createTestSessions()
	now := time.Now()

	// Initially not in combat
	if attacker.IsInCombat(now) {
		t.Fatalf("attacker should not be in combat initially")
	}
	if target.IsInCombat(now) {
		t.Fatalf("target should not be in combat initially")
	}

	dmg := &protocol.DamageNotify{AttackerID: 1, TargetID: 2, Damage: 25.0}
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 25.0 {
		t.Fatalf("expected damage to succeed, got valid=%v, reason=%q", valid, reason)
	}

	// Now both should be in combat
	now = time.Now()
	if !attacker.IsInCombat(now) {
		t.Fatalf("expected attacker to be in combat after attack")
	}
	if !target.IsInCombat(now) {
		t.Fatalf("expected target to be in combat after attack")
	}

	// Combat status should still be active 20 seconds later
	t20 := now.Add(20 * time.Second)
	if !attacker.IsInCombat(t20) {
		t.Fatalf("attacker should still be in combat after 20s")
	}
	if !target.IsInCombat(t20) {
		t.Fatalf("target should still be in combat after 20s")
	}

	// Combat status should expire after 30 seconds
	t35 := now.Add(35 * time.Second)
	if attacker.IsInCombat(t35) {
		t.Fatalf("attacker combat should have expired after 35s")
	}
	if target.IsInCombat(t35) {
		t.Fatalf("target combat should have expired after 35s")
	}
}

func TestDamage_HealthClampingAndDirtyFlag(t *testing.T) {
	dh := NewDamageHandler()
	attacker, target := createTestSessions()

	target.Health = 30.0
	target.Dirty = false

	dmg := &protocol.DamageNotify{AttackerID: 1, TargetID: 2, Damage: 50.0}
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 50.0 {
		t.Fatalf("expected damage 50 to succeed, got valid=%v, applied=%v, reason=%q", valid, applied, reason)
	}

	// Health should not go below 0
	target.Lock()
	health := target.Health
	dirty := target.Dirty
	target.Unlock()

	if health != 0.0 {
		t.Fatalf("expected target health to clamp to 0.0, got %f", health)
	}
	if !dirty {
		t.Fatalf("expected target.Dirty to be true after taking damage")
	}
}
