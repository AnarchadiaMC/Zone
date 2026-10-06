package game

import (
	"testing"

	"zone-online/zone-server/internal/protocol"
)

func TestDamage_BudgetClampsOverOneSecond(t *testing.T) {
	dh := NewDamageHandler()
	attacker, target := createTestSessions()
	attacker.Position = [3]float32{0, 0, 0}
	target.Position = [3]float32{5, 0, 0}
	target.Health = 1000

	total := float32(0)
	rejected := 0
	for i := 0; i < 5; i++ {
		applied, valid, _ := dh.ValidateAndApplyDamage(attacker, target, &protocol.DamageNotify{
			AttackerID: 1, TargetID: 2, Damage: 150,
		})
		if valid {
			total += applied
		} else {
			rejected++
		}
	}
	if total != 400 {
		t.Fatalf("applied over 1s = %v, want budget clamp at 400", total)
	}
	if rejected != 2 {
		t.Fatalf("rejected hits = %d, want 2 after budget exhausted (150+150+100)", rejected)
	}
	if target.Health != 600 {
		t.Fatalf("target health = %v, want 1000-400=600", target.Health)
	}
}

func TestDamage_NormalSustainedDPSPasses(t *testing.T) {
	dh := NewDamageHandler()
	attacker, target := createTestSessions()
	attacker.Position = [3]float32{0, 0, 0}
	target.Position = [3]float32{5, 0, 0}
	target.Health = 1000

	for i := 0; i < 4; i++ {
		applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, &protocol.DamageNotify{
			AttackerID: 1, TargetID: 2, Damage: 90,
		})
		if !valid || applied != 90 {
			t.Fatalf("sustained hit %d rejected: valid=%v applied=%v reason=%q", i, valid, applied, reason)
		}
	}
	if target.Health != 640 {
		t.Fatalf("target health = %v, want 640", target.Health)
	}
}

func TestDamage_NoLOSGeometryDocumented(t *testing.T) {
	// A bare DamageHandler has no occluder checker wired (the Server wires one
	// from los_data_dir); without it a hit through a wall within range passes.
	// This pins the fail-open default so it is deliberate, not accidental.
	dh := NewDamageHandler()
	attacker, target := createTestSessions()
	attacker.Position = [3]float32{0, 0, 0}
	target.Position = [3]float32{2, 0, 0}
	target.Health = 100

	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, &protocol.DamageNotify{
		AttackerID: 1, TargetID: 2, Damage: 30,
	})
	if !valid || applied != 30 {
		t.Fatalf("point-blank hit rejected: valid=%v reason=%q", valid, reason)
	}
}

func BenchmarkDamageHandler_ValidateAndApply(b *testing.B) {
	dh := NewDamageHandler()
	dh.SetDamageBudgetPerS(1e9)
	attacker, target := createTestSessions()
	attacker.Position = [3]float32{0, 0, 0}
	target.Position = [3]float32{5, 0, 0}
	dmg := &protocol.DamageNotify{AttackerID: 1, TargetID: 2, Damage: 50}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		target.Lock()
		target.Health = 1000
		target.Unlock()
		if _, valid, _ := dh.ValidateAndApplyDamage(attacker, target, dmg); !valid {
			b.Fatal("unexpected rejection")
		}
	}
}
