package game

import (
	"strings"
	"testing"
	"time"

	"zone-online/zone-server/internal/network"
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

func TestDamage_LagSwitchWindowRejects(t *testing.T) {
	dh := NewDamageHandler()
	ac := NewAntiCheatManager()
	dh.SetAntiCheatManager(ac)
	attacker, target := createTestSessions()
	attacker.Position = [3]float32{0, 0, 0}
	target.Position = [3]float32{5, 0, 0}

	ac.RecordLagswitchStrike(attacker.SessionID, time.Now())
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, &protocol.DamageNotify{
		AttackerID: 1, TargetID: 2, Damage: 25,
	})
	if valid || applied != 0 {
		t.Fatalf("lag-switch attacker dealt damage: valid=%v applied=%v", valid, applied)
	}
	if reason != "attacker lag-switch window" {
		t.Fatalf("reason = %q, want attacker lag-switch window", reason)
	}
	if target.Health != 100 {
		t.Fatalf("target took damage during lag-switch window: %v", target.Health)
	}
}

func TestDamage_MovementBurstRejects(t *testing.T) {
	dh := NewDamageHandler()
	dh.SetMovementGuard(NewMovementGuard(nil))
	attacker, target := createTestSessions()
	target.Position = [3]float32{110, 0, 0}

	// Accepted history says the attacker was at the origin 500 ms ago, but the
	// server position is now 100 m away: burst-after-stall.
	attacker.Lock()
	attacker.TransformHistory.Push(network.TransformSample{
		At: time.Now().Add(-500 * time.Millisecond),
		X:  0, Y: 0, Z: 0,
	})
	attacker.Position = [3]float32{100, 0, 0}
	attacker.Unlock()

	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, &protocol.DamageNotify{
		AttackerID: 1, TargetID: 2, Damage: 40,
	})
	if valid || applied != 0 {
		t.Fatalf("burst movement dealt damage: valid=%v applied=%v", valid, applied)
	}
	if !strings.Contains(reason, "movement burst") {
		t.Fatalf("reason = %q, want movement burst", reason)
	}
}

func TestDamage_NoLOSGeometryDocumented(t *testing.T) {
	// There is no world geometry server-side: a hit through a wall within range
	// is accepted today. This test pins that known limitation so it is a
	// deliberate choice, not an accident, until server-side level collision
	// data exists.
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
