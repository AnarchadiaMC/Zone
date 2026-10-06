package game

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/ai"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// newAICombatServer builds a DB-less, AI+combat enabled server with a recording
// sink. mutate can override any config key (combat disabled, custom cooldown,
// corpse seconds, ...).
func newAICombatServer(t *testing.T, mutate func(*config.Config)) (*Server, *fakeSink) {
	t.Helper()
	enabled := true
	disabledLOS := false
	cfg := &config.Config{
		AIEnabled:          &enabled,
		AIEnterRadiusM:     220,
		AILeaveRadiusM:     220,
		AIMaxEntities:      64,
		AICombatEnabled:    &enabled,
		AIAggroRadiusM:     40,
		AIAttackRangeM:     2,
		AIAttackCooldownMS: 1500,
		AIMeleeDamage:      10,
		AICorpseSeconds:    5,
		AIPatrolResumeS:    10,
		LosEnabled:         &disabledLOS,
	}
	if mutate != nil {
		mutate(cfg)
	}
	sink := &fakeSink{}
	activeSink = sink
	s := NewServer(cfg, nil, zap.NewNop())
	s.udp = sink.UDPListener()
	return s, sink
}

func aiCombatPlayer(t *testing.T, s *Server, id uint32, port int, pos [3]float32, faction string) *network.PlayerSession {
	t.Helper()
	sess := addAIPlayer(t, s, id, port, "l01_escape", pos)
	sess.Lock()
	sess.Faction = faction
	sess.Unlock()
	return sess
}

func registerCombatPuppet(t *testing.T, s *Server, faction string, spawn [3]float32) *ai.Squad {
	t.Helper()
	sq := s.registerAIPuppet(ai.PuppetDef{
		Label: "Combatant", Section: "sim_default_bandit_0", Faction: faction,
		Level: "l01_escape", Spawn: spawn, PatrolRadius: 5, WalkSpeed: 1.5, RunSpeed: 3.0,
	})
	if sq == nil {
		t.Fatal("registerAIPuppet returned nil")
	}
	return sq
}

func puppetStateOf(t *testing.T, s *Server, squadID uint32) ai.PuppetState {
	t.Helper()
	state, ok := s.squads.GetPuppetState(squadID)
	if !ok {
		t.Fatalf("puppet %d not found", squadID)
	}
	return state
}

func sendDamageNotify(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, targetID uint32, damage float32) {
	t.Helper()
	dmg := protocol.DamageNotify{TargetID: targetID, AttackerID: sess.SessionID, Damage: damage}
	s.HandlePacket(buildTestPacket(t, protocol.OpDamageNotify, seq, protocol.FlagReliable, dmg), sess.UDPAddr)
}

// Aggro -> chase at run speed -> attack in range, with the validated melee hit
// relayed to the victim as OpDamageNotify carrying the AI entity id.
func TestAICombat_ChaseAttackRelayAndCooldown(t *testing.T) {
	s, sink := newAICombatServer(t, nil)
	player := aiCombatPlayer(t, s, 8801, 48001, [3]float32{2.5, 0, 0}, "stalker")
	sq := registerCombatPuppet(t, s, "bandit", [3]float32{0, 0, 0})
	entityID, ok := s.AIEntityIDOf(sq.ID)
	if !ok || entityID < AIEntityIDBase {
		t.Fatalf("entity id = %d ok=%v, want >= %d", entityID, ok, AIEntityIDBase)
	}
	base := time.Unix(1_700_000_000, 0)

	sink.Reset()
	s.tickAIReplication(base) // acquire at 2.5 m
	if got := puppetStateOf(t, s, sq.ID); got.State != ai.AIStateChase {
		t.Fatalf("state at 2.5 m = %v, want Chase", got.State)
	}
	s.tickAIReplication(base.Add(250 * time.Millisecond)) // one clamped step
	if got := puppetStateOf(t, s, sq.ID); got.Position[0] < 0.7 || got.Position[0] > 0.8 {
		t.Fatalf("chase position = %v, want ~0.75 after one 250 ms step", got.Position)
	}

	// Second step puts the player inside attack range and the FSM swings.
	sink.Reset()
	s.tickAIReplication(base.Add(500 * time.Millisecond))

	relays := packetsByOpcode(sink, protocol.OpDamageNotify)
	if len(relays) != 1 {
		t.Fatalf("melee relays = %d, want 1", len(relays))
	}
	var out protocol.DamageNotify
	if err := binary.Read(bytes.NewReader(packetPayload(t, relays[0])), binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode relay: %v", err)
	}
	if out.AttackerID != entityID || out.TargetID != player.SessionID {
		t.Fatalf("relay ids = attacker %d target %d, want %d/%d", out.AttackerID, out.TargetID, entityID, player.SessionID)
	}
	if out.Damage != 10 {
		t.Fatalf("relay damage = %v, want 10", out.Damage)
	}
	player.Lock()
	health := player.Health
	player.Unlock()
	if health != 90 {
		t.Fatalf("player health after melee = %v, want 90", health)
	}

	// 100 ms later: cooldown still active.
	sink.Reset()
	s.tickAIReplication(base.Add(600 * time.Millisecond))
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 0 {
		t.Fatalf("cooldown relay = %d, want 0", got)
	}
	// Past ai_attack_cooldown_ms: another swing.
	s.tickAIReplication(base.Add(2 * time.Second))
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 1 {
		t.Fatalf("post-cooldown relay = %d, want 1", got)
	}
	player.Lock()
	health = player.Health
	player.Unlock()
	if health != 80 {
		t.Fatalf("player health after second melee = %v, want 80", health)
	}
}

// Faction relations gate aggro: same faction and neutral relations keep the
// squad patrolling; mutant (empty faction) squads are hostile to everyone.
func TestAICombat_HostilityGating(t *testing.T) {
	cases := []struct {
		name           string
		squadFaction   string
		playerFaction  string
		wantEngagement bool
	}{
		{"same faction", "stalker", "stalker", false},
		{"neutral relation", "stalker", "dolg", false},
		{"enemy relation", "bandit", "stalker", true},
		{"mutant hostile to all", "", "stalker", true},
		{"monster hostile to all", "monster", "dolg", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newAICombatServer(t, nil)
			aiCombatPlayer(t, s, 8811, 48011, [3]float32{0, 0, 0}, tc.playerFaction)
			sq := registerCombatPuppet(t, s, tc.squadFaction, [3]float32{5, 0, 0})
			base := time.Unix(1_700_000_000, 0)
			s.tickAIReplication(base)
			state := puppetStateOf(t, s, sq.ID)
			engaged := state.State == ai.AIStateChase || state.State == ai.AIStateAttack
			if engaged != tc.wantEngagement {
				t.Fatalf("engaged = %v (state %v), want %v", engaged, state.State, tc.wantEngagement)
			}
		})
	}
}

// A player in a safe zone is never targeted, and a puppet standing in a safe
// zone never engages.
func TestAICombat_SafezoneSuppressesTargeting(t *testing.T) {
	s, _ := newAICombatServer(t, nil)
	// Cordon rookie safe zone centre; the puppet sits 10 m outside it.
	player := aiCombatPlayer(t, s, 8821, 48021, [3]float32{-211.3, -20.2, -145.8}, "stalker")
	sq := registerCombatPuppet(t, s, "bandit", [3]float32{-201.3, -20.2, -145.8})
	base := time.Unix(1_700_000_000, 0)
	s.tickAIReplication(base)
	if got := puppetStateOf(t, s, sq.ID); got.State != ai.AIStatePatrol {
		t.Fatalf("safe-zoned player targeted: state %v, want Patrol", got.State)
	}
	player.Lock()
	health := player.Health
	player.Unlock()
	if health != 100 {
		t.Fatalf("safe-zoned player health = %v, want 100", health)
	}
}

// The combat-disabled flag keeps the patrol simulation but suppresses aggro,
// movement toward players and attacks.
func TestAICombat_DisabledFlagKeepsPatrol(t *testing.T) {
	disabled := false
	s, sink := newAICombatServer(t, func(c *config.Config) { c.AICombatEnabled = &disabled })
	player := aiCombatPlayer(t, s, 8831, 48031, [3]float32{0, 0, 0}, "stalker")
	sq := registerCombatPuppet(t, s, "bandit", [3]float32{5, 0, 0})
	base := time.Unix(1_700_000_000, 0)
	sink.Reset()
	for i := 0; i < 3; i++ {
		s.tickAIReplication(base.Add(time.Duration(i) * time.Second))
	}
	if got := puppetStateOf(t, s, sq.ID); got.State != ai.AIStatePatrol {
		t.Fatalf("state with ai_combat_enabled=false = %v, want Patrol", got.State)
	}
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 0 {
		t.Fatalf("damage with ai_combat_enabled=false = %d, want 0", got)
	}
	player.Lock()
	health := player.Health
	player.Unlock()
	if health != 100 {
		t.Fatalf("player health with combat disabled = %v, want 100", health)
	}
}

// A player->AI hit is validated, applied to the puppet HP, reflected in the
// next ENTITY_ENTER, and lethal damage runs the full corpse lifecycle with
// ENTITY_LEAVE after ai_corpse_seconds.
func TestAICombat_PlayerKillsPuppetLifecycle(t *testing.T) {
	s, sink := newAICombatServer(t, nil)
	player := aiCombatPlayer(t, s, 8841, 48041, [3]float32{0, 0, 0}, "stalker")
	sq := registerCombatPuppet(t, s, "bandit", [3]float32{10, 0, 0})
	entityID, _ := s.AIEntityIDOf(sq.ID)

	base := time.Unix(1_700_000_000, 0)
	s.tickAIReplication(base) // puppet visible, streaming
	sink.Reset()

	// First non-lethal hit: 100 -> 40, health update broadcast.
	sendDamageNotify(t, s, player, 1, entityID, 60)
	if got := puppetStateOf(t, s, sq.ID); got.Health != 40 {
		t.Fatalf("puppet health after 60 damage = %v, want 40", got.Health)
	}
	healthEnters := entityEnterPacketsFor(t, sink, entityID)
	if len(healthEnters) == 0 || healthEnters[len(healthEnters)-1].Health != 40 {
		t.Fatalf("health ENTITY_ENTER = %+v, want last Health 40", healthEnters)
	}

	// Lethal hit.
	sendDamageNotify(t, s, player, 2, entityID, 60)
	state := puppetStateOf(t, s, sq.ID)
	if state.Health != 0 || state.State != ai.AIStateDead {
		t.Fatalf("killed puppet = health %v state %v, want 0/Dead", state.Health, state.State)
	}

	// Death animation streams to nearby players.
	sink.Reset()
	s.tickAIReplication(base.Add(100 * time.Millisecond))
	deathSeen := false
	for _, st := range readAIStates(t, sink) {
		if st.EntityID == entityID && st.Anim == protocol.AIAnimDeath {
			deathSeen = true
		}
	}
	if !deathSeen {
		t.Fatal("dead puppet never streamed AnimDeath")
	}

	// Still inside the 5 s corpse window: entity registered, no leave.
	sink.Reset()
	s.tickAIReplication(base.Add(3 * time.Second))
	if _, ok := s.AIEntityIDOf(sq.ID); !ok {
		t.Fatal("entity released before the corpse delay")
	}
	if got := len(entityLeavePacketsFor(t, sink, entityID)); got != 0 {
		t.Fatalf("leave before corpse delay = %d, want 0", got)
	}

	// Past the delay: ENTITY_LEAVE and release.
	sink.Reset()
	s.tickAIReplication(base.Add(6 * time.Second))
	if got := len(entityLeavePacketsFor(t, sink, entityID)); got != 1 {
		t.Fatalf("leave after corpse delay = %d, want 1", got)
	}
	if _, ok := s.AIEntityIDOf(sq.ID); ok {
		t.Fatal("entity id still registered after corpse release")
	}
}

// ai_corpse_seconds is honored for the post-death entity release.
func TestAICombat_CorpseSecondsConfig(t *testing.T) {
	s, _ := newAICombatServer(t, func(c *config.Config) { c.AICorpseSeconds = 2 })
	player := aiCombatPlayer(t, s, 8851, 48051, [3]float32{0, 0, 0}, "stalker")
	sq := registerCombatPuppet(t, s, "bandit", [3]float32{10, 0, 0})
	entityID, _ := s.AIEntityIDOf(sq.ID)
	base := time.Unix(1_700_000_000, 0)
	s.tickAIReplication(base)
	sendDamageNotify(t, s, player, 1, entityID, 100)

	s.tickAIReplication(base.Add(100 * time.Millisecond)) // deadSince
	s.tickAIReplication(base.Add(1500 * time.Millisecond))
	if _, ok := s.AIEntityIDOf(sq.ID); !ok {
		t.Fatal("entity released inside the configured 2 s corpse window")
	}
	s.tickAIReplication(base.Add(2500 * time.Millisecond))
	if _, ok := s.AIEntityIDOf(sq.ID); ok {
		t.Fatal("entity not released after the configured 2 s corpse window")
	}
}

// Out-of-range player->AI damage and forged attacker ids are rejected by the
// shared validation path.
func TestAICombat_PlayerDamageValidation(t *testing.T) {
	s, sink := newAICombatServer(t, nil)
	player := aiCombatPlayer(t, s, 8861, 48061, [3]float32{0, 0, 0}, "stalker")
	sq := registerCombatPuppet(t, s, "bandit", [3]float32{400, 0, 0}) // beyond 300 m
	entityID, _ := s.AIEntityIDOf(sq.ID)
	base := time.Unix(1_700_000_000, 0)
	s.tickAIReplication(base)
	sink.Reset()

	sendDamageNotify(t, s, player, 1, entityID, 30)
	if got := puppetStateOf(t, s, sq.ID); got.Health != 100 {
		t.Fatalf("out-of-range hit changed puppet health to %v, want 100", got.Health)
	}

	// Forged attacker id: packet claims a different attacker than the sender.
	forged := protocol.DamageNotify{TargetID: entityID, AttackerID: player.SessionID + 1, Damage: 30}
	s.HandlePacket(buildTestPacket(t, protocol.OpDamageNotify, 2, protocol.FlagReliable, forged), player.UDPAddr)
	if got := puppetStateOf(t, s, sq.ID); got.Health != 100 {
		t.Fatalf("forged attacker hit changed puppet health to %v, want 100", got.Health)
	}
}

// entityLeavePacketsFor filters ENTITY_LEAVE_AOI payloads by entity id.
func entityLeavePacketsFor(t *testing.T, sink *fakeSink, entityID uint32) []protocol.EntityLeaveAoI {
	t.Helper()
	var out []protocol.EntityLeaveAoI
	for _, raw := range packetsByOpcode(sink, protocol.OpEntityLeaveAoI) {
		var leave protocol.EntityLeaveAoI
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &leave); err != nil {
			continue
		}
		if leave.EntityID == entityID {
			out = append(out, leave)
		}
	}
	return out
}
