package ai

import (
	"testing"
	"time"
)

// hostileAll is the simplest relation gate: everything is hostile.
func hostileAll(string, string) bool { return true }

func combatPuppet(t *testing.T, sm *SquadManager, spawn [3]float32, run float32) *Squad {
	t.Helper()
	sq := sm.RegisterPuppet(PuppetDef{
		Label: "Tester", Section: "sim_default_bandit_0", Faction: "bandit",
		Level: "l01_escape", Spawn: spawn, PatrolRadius: 5,
		WalkSpeed: 1.5, RunSpeed: run,
	})
	if sq == nil {
		t.Fatal("RegisterPuppet returned nil")
	}
	return sq
}

func combatTarget(id uint32, pos [3]float32) CombatTarget {
	return CombatTarget{ID: id, Level: "l01_escape", Position: pos, Faction: "stalker"}
}

func puppetByID(t *testing.T, sm *SquadManager, id uint32) PuppetState {
	t.Helper()
	state, ok := sm.GetPuppetState(id)
	if !ok {
		t.Fatalf("puppet %d not found", id)
	}
	return state
}

// A hostile target inside the aggro radius is acquired and pursued at run
// speed; a target beyond it is ignored.
func TestCombat_AcquiresInsideAggroRadiusAndChases(t *testing.T) {
	sm := NewSquadManager()
	sq := combatPuppet(t, sm, [3]float32{0, 0, 0}, 3.0)
	params := CombatParams{AggroRadius: 40, AttackRange: 2, AttackCooldown: time.Second, MeleeDamage: 10, PatrolResume: 10 * time.Second}
	base := time.Unix(1_700_000_000, 0)

	// Outside the aggro radius: stays on patrol.
	sm.TickPuppetsWithCombat(base, map[uint32]bool{sq.ID: true}, CombatStep{
		Enabled: true, Params: params, Hostile: hostileAll,
		Targets: []CombatTarget{combatTarget(7, [3]float32{100, 0, 0})},
	})
	if got := puppetByID(t, sm, sq.ID); got.State != AIStatePatrol {
		t.Fatalf("state at 100 m = %v, want Patrol", got.State)
	}

	// Inside the aggro radius: acquires and chases.
	sm.TickPuppetsWithCombat(base, map[uint32]bool{sq.ID: true}, CombatStep{
		Enabled: true, Params: params, Hostile: hostileAll,
		Targets: []CombatTarget{combatTarget(7, [3]float32{10, 0, 0})},
	})
	if got := puppetByID(t, sm, sq.ID); got.State != AIStateChase {
		t.Fatalf("state at 10 m = %v, want Chase", got.State)
	}

	// One online step (clamped to 250 ms) moves ~run speed * 0.25 toward the
	// target.
	sm.TickPuppetsWithCombat(base.Add(time.Second), map[uint32]bool{sq.ID: true}, CombatStep{
		Enabled: true, Params: params, Hostile: hostileAll,
		Targets: []CombatTarget{combatTarget(7, [3]float32{10, 0, 0})},
	})
	got := puppetByID(t, sm, sq.ID)
	if got.State != AIStateChase {
		t.Fatalf("state after chase step = %v, want Chase", got.State)
	}
	if got.Anim != AnimRun {
		t.Fatalf("anim while chasing = %d, want AnimRun", got.Anim)
	}
	if got.Position[0] < 0.7 || got.Position[0] > 0.8 {
		t.Fatalf("chase displacement = %v, want ~0.75 m (3 m/s * 250 ms step clamp)", got.Position)
	}
}

// The hostility callback gates acquisition: a callback that rejects the target
// keeps the squad patrolling and produces no attacks.
func TestCombat_HostilityGate(t *testing.T) {
	sm := NewSquadManager()
	sq := combatPuppet(t, sm, [3]float32{0, 0, 0}, 3.0)
	params := CombatParams{AggroRadius: 40, AttackRange: 2, AttackCooldown: time.Second, MeleeDamage: 10, PatrolResume: time.Second}
	base := time.Unix(1_700_000_000, 0)

	for i := 0; i < 3; i++ {
		_, attacks := sm.TickPuppetsWithCombat(base.Add(time.Duration(i)*time.Second), map[uint32]bool{sq.ID: true}, CombatStep{
			Enabled: true, Params: params,
			Hostile: func(string, string) bool { return false },
			Targets: []CombatTarget{combatTarget(7, [3]float32{5, 0, 0})},
		})
		if len(attacks) != 0 {
			t.Fatalf("tick %d produced %d attacks for a non-hostile target", i, len(attacks))
		}
	}
	if got := puppetByID(t, sm, sq.ID); got.State != AIStatePatrol {
		t.Fatalf("state with non-hostile target = %v, want Patrol", got.State)
	}
}

// Attack cadence: one swing on entry plus one per cooldown, no faster.
func TestCombat_AttackCooldown(t *testing.T) {
	sm := NewSquadManager()
	sq := combatPuppet(t, sm, [3]float32{0, 0, 0}, 3.0)
	params := CombatParams{AggroRadius: 40, AttackRange: 2, AttackCooldown: time.Second, MeleeDamage: 10, PatrolResume: time.Second}
	base := time.Unix(1_700_000_000, 0)
	online := map[uint32]bool{sq.ID: true}
	targets := []CombatTarget{combatTarget(7, [3]float32{1, 0, 0})}
	step := CombatStep{Enabled: true, Params: params, Hostile: hostileAll, Targets: targets}

	_, attacks := sm.TickPuppetsWithCombat(base, online, step)
	if len(attacks) != 1 {
		t.Fatalf("first tick attacks = %d, want 1", len(attacks))
	}
	if attacks[0].TargetID != 7 || attacks[0].Damage != 10 {
		t.Fatalf("attack event = %+v, want target 7 damage 10", attacks[0])
	}
	if got := puppetByID(t, sm, sq.ID); got.State != AIStateAttack || got.Anim != AnimAttack {
		t.Fatalf("state/anim after first swing = %v/%d, want Attack/%d", got.State, got.Anim, AnimAttack)
	}

	_, attacks = sm.TickPuppetsWithCombat(base.Add(100*time.Millisecond), online, step)
	if len(attacks) != 0 {
		t.Fatalf("attack 100 ms later = %d, want 0 (cooldown)", len(attacks))
	}

	_, attacks = sm.TickPuppetsWithCombat(base.Add(time.Second), online, step)
	if len(attacks) != 1 {
		t.Fatalf("attack after cooldown = %d, want 1", len(attacks))
	}
}

// Losing the target (removed from the list, e.g. death/safe-zone) drops to
// Alert and only resumes Patrol after the configured delay.
func TestCombat_LostTargetAlertsThenResumesPatrol(t *testing.T) {
	sm := NewSquadManager()
	sq := combatPuppet(t, sm, [3]float32{0, 0, 0}, 3.0)
	params := CombatParams{AggroRadius: 40, AttackRange: 2, AttackCooldown: time.Second, MeleeDamage: 10, PatrolResume: 10 * time.Second}
	base := time.Unix(1_700_000_000, 0)
	online := map[uint32]bool{sq.ID: true}
	engaged := CombatStep{Enabled: true, Params: params, Hostile: hostileAll, Targets: []CombatTarget{combatTarget(7, [3]float32{10, 0, 0})}}
	gone := CombatStep{Enabled: true, Params: params, Hostile: hostileAll}

	sm.TickPuppetsWithCombat(base, online, engaged)
	sm.TickPuppetsWithCombat(base.Add(time.Second), online, engaged)
	if got := puppetByID(t, sm, sq.ID); got.State != AIStateChase {
		t.Fatalf("state while engaged = %v, want Chase", got.State)
	}

	sm.TickPuppetsWithCombat(base.Add(2*time.Second), online, gone)
	if got := puppetByID(t, sm, sq.ID); got.State != AIStateAlert {
		t.Fatalf("state after losing target = %v, want Alert", got.State)
	}

	// Inside the resume window: still Alert.
	sm.TickPuppetsWithCombat(base.Add(6*time.Second), online, gone)
	if got := puppetByID(t, sm, sq.ID); got.State != AIStateAlert {
		t.Fatalf("state at 4 s into alert = %v, want Alert", got.State)
	}

	// Past ai_patrol_resume_s: Patrol again.
	sm.TickPuppetsWithCombat(base.Add(13*time.Second), online, gone)
	if got := puppetByID(t, sm, sq.ID); got.State != AIStatePatrol {
		t.Fatalf("state after resume window = %v, want Patrol", got.State)
	}
}

// A dead puppet stops attacking and is released after the configured corpse
// delay.
func TestCombat_CorpseDelayAndDeadNoAttacks(t *testing.T) {
	sm := NewSquadManager()
	sm.SetCorpseDelay(2 * time.Second)
	sq := combatPuppet(t, sm, [3]float32{0, 0, 0}, 3.0)
	params := CombatParams{AggroRadius: 40, AttackRange: 2, AttackCooldown: time.Second, MeleeDamage: 10, PatrolResume: time.Second}
	base := time.Unix(1_700_000_000, 0)
	online := map[uint32]bool{sq.ID: true}
	step := CombatStep{Enabled: true, Params: params, Hostile: hostileAll, Targets: []CombatTarget{combatTarget(7, [3]float32{1, 0, 0})}}

	sm.TickPuppetsWithCombat(base, online, step)
	if _, dead, ok := sm.ApplyPuppetDamage(sq.ID, 100); !ok || !dead {
		t.Fatalf("ApplyPuppetDamage ok=%v dead=%v, want true/true", ok, dead)
	}

	// Dead: no attacks and AnimDeath.
	_, attacks := sm.TickPuppetsWithCombat(base.Add(100*time.Millisecond), online, step)
	if len(attacks) != 0 {
		t.Fatalf("dead puppet produced %d attacks, want 0", len(attacks))
	}
	if got := puppetByID(t, sm, sq.ID); got.Anim != AnimDeath {
		t.Fatalf("dead anim = %d, want AnimDeath", got.Anim)
	}

	// Inside the 2 s corpse window the squad is still present.
	despawned, _ := sm.TickPuppetsWithCombat(base.Add(1500*time.Millisecond), online, step)
	if len(despawned) != 0 || sm.PuppetCount() != 1 {
		t.Fatalf("despawned inside corpse window: %v, count=%d", despawned, sm.PuppetCount())
	}

	// Past the window it is reported and removed.
	despawned, _ = sm.TickPuppetsWithCombat(base.Add(2500*time.Millisecond), online, step)
	if len(despawned) != 1 || despawned[0] != sq.ID {
		t.Fatalf("despawned = %v, want [%d]", despawned, sq.ID)
	}
	if sm.PuppetCount() != 0 {
		t.Fatalf("PuppetCount after corpse release = %d, want 0", sm.PuppetCount())
	}
}

// ApplyPuppetDamage is idempotent for already-dead squads and clamps at zero.
func TestCombat_ApplyPuppetDamageClampsAndIgnoresDead(t *testing.T) {
	sm := NewSquadManager()
	sq := combatPuppet(t, sm, [3]float32{0, 0, 0}, 3.0)

	health, dead, ok := sm.ApplyPuppetDamage(sq.ID, 30)
	if !ok || dead || health != 70 {
		t.Fatalf("first hit = %v/%v/%v, want 70/false/true", health, dead, ok)
	}
	health, dead, ok = sm.ApplyPuppetDamage(sq.ID, 1000)
	if !ok || !dead || health != 0 {
		t.Fatalf("lethal hit = %v/%v/%v, want 0/true/true", health, dead, ok)
	}
	if _, _, ok := sm.ApplyPuppetDamage(sq.ID, 10); ok {
		t.Fatal("damage applied to an already-dead puppet")
	}
	if _, _, ok := sm.ApplyPuppetDamage(uint32(9999), 10); ok {
		t.Fatal("damage applied to an unknown squad")
	}
}

// The no-combat entry point must not move or re-state a non-dead puppet.
func TestCombat_DisabledSimulationLeavesPatrol(t *testing.T) {
	sm := NewSquadManager()
	sq := combatPuppet(t, sm, [3]float32{0, 0, 0}, 3.0)
	base := time.Unix(1_700_000_000, 0)
	sm.TickPuppets(base, map[uint32]bool{sq.ID: true})
	sm.TickPuppets(base.Add(time.Second), map[uint32]bool{sq.ID: true})
	if got := puppetByID(t, sm, sq.ID); got.State != AIStatePatrol {
		t.Fatalf("state in patrol-only tick = %v, want Patrol", got.State)
	}
}
