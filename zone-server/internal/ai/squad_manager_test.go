package ai

import (
	"sync"
	"testing"
	"time"
)

// TestAIStateString pins the human-readable names used in logs and tests.
func TestAIStateString(t *testing.T) {
	if AIStateIdle.String() != "Idle" {
		t.Errorf("unexpected string for AIStateIdle: %s", AIStateIdle.String())
	}
	if AIStatePatrol.String() != "Patrol" {
		t.Errorf("unexpected string for AIStatePatrol: %s", AIStatePatrol.String())
	}
	if AIStateAttack.String() != "Attack" {
		t.Errorf("unexpected string for AIStateAttack: %s", AIStateAttack.String())
	}
	if AIStateFlee.String() != "Flee" {
		t.Errorf("unexpected string for AIStateFlee: %s", AIStateFlee.String())
	}
	if AIStateDead.String() != "Dead" {
		t.Errorf("unexpected string for AIStateDead: %s", AIStateDead.String())
	}
	if AIState(99).String() != "Unknown" {
		t.Errorf("unexpected string for invalid AIState: %s", AIState(99).String())
	}
}

// TestPuppetDeadLifecycle pins the dead-squad contract: AnimDeath is
// broadcast, the squad stays for puppetDespawnDelay, then TickPuppets reports
// it despawned (and removes it).
func TestPuppetDeadLifecycle(t *testing.T) {
	sm := NewSquadManager()
	sq := sm.RegisterPuppet(PuppetDef{
		Label: "Doomed", Section: "sim_default_stalker_0", Faction: "stalker",
		Level: "l01_escape", Spawn: [3]float32{10, 0, 0}, PatrolRadius: 5, WalkSpeed: 1.5,
	})
	if sq == nil {
		t.Fatal("RegisterPuppet returned nil")
	}
	sm.SetSquadState(sq.ID, AIStateDead)

	base := time.Unix(1_700_000_000, 0)
	if got := sm.TickPuppets(base, nil); len(got) != 0 {
		t.Fatalf("despawned immediately = %v, want none", got)
	}
	states := sm.SnapshotPuppets()
	if len(states) != 1 || states[0].Anim != AnimDeath {
		t.Fatalf("dead state = %+v, want one squad with AnimDeath", states)
	}

	// Still inside the despawn delay.
	if got := sm.TickPuppets(base.Add(4*time.Second), nil); len(got) != 0 {
		t.Fatalf("despawned before delay = %v", got)
	}
	if sm.PuppetCount() != 1 {
		t.Fatalf("PuppetCount before delay = %d, want 1", sm.PuppetCount())
	}

	// Past the delay: reported and removed.
	despawned := sm.TickPuppets(base.Add(6*time.Second), nil)
	if len(despawned) != 1 || despawned[0] != sq.ID {
		t.Fatalf("despawned = %v, want [%d]", despawned, sq.ID)
	}
	if sm.PuppetCount() != 0 {
		t.Fatalf("PuppetCount after despawn = %d, want 0", sm.PuppetCount())
	}
	if _, ok := sm.GetSquad(sq.ID); ok {
		t.Fatalf("despawned squad still resolvable")
	}
}

// TestPuppetRunAnimationDerivedFromRunUntil verifies Anim matches the chosen
// speed when the optional run window is active.
func TestPuppetRunAnimationDerivedFromRunUntil(t *testing.T) {
	sm := NewSquadManager()
	sq := sm.RegisterPuppet(PuppetDef{
		Label: "Runner", Section: "sim_default_stalker_0", Faction: "stalker",
		Level: "l01_escape", Spawn: [3]float32{0, 0, 0}, PatrolRadius: 20, WalkSpeed: 1.0, RunSpeed: 4.0,
	})
	base := time.Unix(1_700_000_000, 0)
	sm.TickPuppets(base, map[uint32]bool{sq.ID: true}) // seed lastStep

	sm.SetPuppetRun(sq.ID, base.Add(10*time.Second))
	sm.TickPuppets(base.Add(1*time.Second), map[uint32]bool{sq.ID: true})
	if got := sm.SnapshotPuppets()[0]; got.Anim != AnimRun {
		t.Fatalf("Anim during run window = %d, want AnimRun", got.Anim)
	}

	// Run window expired -> walk animation.
	sm.TickPuppets(base.Add(11*time.Second), map[uint32]bool{sq.ID: true})
	if got := sm.SnapshotPuppets()[0]; got.Anim != AnimWalk {
		t.Fatalf("Anim after run window = %d, want AnimWalk", got.Anim)
	}
}

// TestConcurrentAccess_Race verifies thread safety of SquadManager under concurrent access
// using go test -race.
func TestConcurrentAccess_Race(t *testing.T) {
	sm := NewSquadManager()

	const numWorkers = 16
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (workerID + i) % 6 {
				case 0:
					// Register a puppet
					sm.RegisterPuppet(PuppetDef{
						Label: "Worker", Section: "sim_default_stalker_0", Faction: "stalker",
						Level: "l01_escape", Spawn: [3]float32{float32(i * 10), 0, float32(i * 10)},
					})
				case 1:
					// Read all puppets and count
					_ = sm.PuppetCount()
					all := sm.SnapshotPuppets()
					if len(all) > 0 {
						_, _ = sm.GetSquad(all[0].ID)
					}
				case 2:
					// Modify state
					all := sm.SnapshotPuppets()
					if len(all) > 0 {
						target := all[i%len(all)]
						states := []AIState{AIStateIdle, AIStatePatrol, AIStateAttack, AIStateFlee, AIStateDead}
						sm.SetSquadState(target.ID, states[i%len(states)])
					}
				case 3:
					// Advance puppets; safe to query the manager from the caller.
					sm.TickPuppets(time.Now(), nil)
					_, _ = sm.GetSquad(uint32(i))
				case 4:
					// Despawn squads
					all := sm.SnapshotPuppets()
					if len(all) > 5 {
						sm.DespawnSquad(all[0].ID)
					}
				case 5:
					// Get non-existent
					_, _ = sm.GetSquad(uint32(99999 + i))
				}
			}
		}()
	}

	wg.Wait()
}
