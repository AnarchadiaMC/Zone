package game

import (
	"bytes"
	"encoding/binary"
	"math"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/ai"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// newAIPuppetServer builds a DB-less, AI-enabled server with a recording sink
// so tests control the exact puppet set.
func newAIPuppetServer(t *testing.T, onlineRadiusM float64) (*Server, *fakeSink) {
	t.Helper()
	enabled := true
	cfg := &config.Config{AIEnabled: &enabled, AIOnlineRadiusM: onlineRadiusM}
	sink := &fakeSink{}
	activeSink = sink
	s := NewServer(cfg, nil, zap.NewNop())
	s.udp = sink.UDPListener()
	return s, sink
}

func addAIPlayer(t *testing.T, s *Server, id uint32, port int, level string, pos [3]float32) *network.PlayerSession {
	t.Helper()
	sess := &network.PlayerSession{
		SessionID:    id,
		AccountID:    "uuid-ai-test",
		Name:         "AITester",
		UDPAddr:      &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port},
		CurrentLevel: level,
		Position:     pos,
		Health:       100,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)
	return sess
}

func readEntityEnter(t *testing.T, raw []byte) protocol.EntityEnterAoI {
	t.Helper()
	var pkt protocol.EntityEnterAoI
	if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &pkt); err != nil {
		t.Fatalf("read EntityEnterAoI: %v", err)
	}
	return pkt
}

func readEntityLeave(t *testing.T, raw []byte) protocol.EntityLeaveAoI {
	t.Helper()
	var pkt protocol.EntityLeaveAoI
	if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &pkt); err != nil {
		t.Fatalf("read EntityLeaveAoI: %v", err)
	}
	return pkt
}

func readAIStates(t *testing.T, sinks *fakeSink) []protocol.AIStateEntry {
	t.Helper()
	var out []protocol.AIStateEntry
	for _, raw := range packetsByOpcode(sinks, protocol.OpAIState) {
		pkt, err := protocol.ReadAIStatePacket(bytes.NewReader(packetPayload(t, raw)))
		if err != nil {
			t.Fatalf("ReadAIStatePacket: %v", err)
		}
		if int(pkt.Count) > protocol.MaxAIStateEntries {
			t.Fatalf("AI state count %d exceeds wire cap %d", pkt.Count, protocol.MaxAIStateEntries)
		}
		out = append(out, pkt.Entries[:int(pkt.Count)]...)
	}
	return out
}

// A puppet crossing the online radius must produce exactly one ENTITY_ENTER_AOI
// on the way in and exactly one ENTITY_LEAVE_AOI on the way out, while
// OpAIState streams every tick it is in range.
func TestAI_AoIBoundariesExactlyOnce(t *testing.T) {
	s, sink := newAIPuppetServer(t, 100.0)
	addAIPlayer(t, s, 9001, 46001, "l01_escape", [3]float32{0, 0, 0})

	sq := s.registerAIPuppet(ai.PuppetDef{
		Label:        "Boundary Squad",
		Section:      "sim_default_stalker_0",
		Faction:      "stalker",
		Level:        "l01_escape",
		Spawn:        [3]float32{100, 0, 0}, // exactly on the radius: inside
		PatrolRadius: 10,
		WalkSpeed:    1.5,
	})
	if sq == nil {
		t.Fatal("RegisterPuppet returned nil")
	}
	entityID, ok := s.AIEntityIDOf(sq.ID)
	if !ok || entityID < AIEntityIDBase {
		t.Fatalf("entity id = %d ok=%v, want >= %d", entityID, ok, AIEntityIDBase)
	}

	now := time.Now()
	sink.Reset()
	s.tickAIReplication(now)

	enters := packetsByOpcode(sink, protocol.OpEntityEnterAoI)
	if len(enters) != 1 {
		t.Fatalf("first tick enter packets = %d, want 1", len(enters))
	}
	enter := readEntityEnter(t, enters[0])
	if enter.EntityID != entityID {
		t.Fatalf("enter EntityID = %d, want %d", enter.EntityID, entityID)
	}
	if enter.EntityType != 0 {
		t.Errorf("enter EntityType = %d, want 0 (AI)", enter.EntityType)
	}
	if got := string(bytes.TrimRight(enter.Section[:], "\x00")); got != "sim_default_stalker_0" {
		t.Errorf("enter Section = %q, want sim_default_stalker_0", got)
	}
	if got := string(bytes.TrimRight(enter.Name[:], "\x00")); got != "Boundary Squad" {
		t.Errorf("enter Name = %q, want Boundary Squad", got)
	}
	if enter.Health != 100 || enter.Gvid != 0 {
		t.Errorf("enter Health/Gvid = %d/%d, want 100/0", enter.Health, enter.Gvid)
	}
	if got := len(readAIStates(t, sink)); got != 1 {
		t.Fatalf("state entries after enter = %d, want 1", got)
	}

	// Second tick at the same timestamp must not re-enter but must keep
	// streaming state.
	sink.Reset()
	s.tickAIReplication(now)
	if got := len(packetsByOpcode(sink, protocol.OpEntityEnterAoI)); got != 0 {
		t.Fatalf("duplicate enter packets = %d, want 0", got)
	}
	if got := len(readAIStates(t, sink)); got != 1 {
		t.Fatalf("state entries on second tick = %d, want 1", got)
	}

	// Leave the radius: exactly one leave, no state afterwards.
	if !s.squads.SetPuppetPosition(sq.ID, 150, 0, 0) {
		t.Fatal("SetPuppetPosition failed")
	}
	sink.Reset()
	s.tickAIReplication(now)
	leaves := packetsByOpcode(sink, protocol.OpEntityLeaveAoI)
	if len(leaves) != 1 {
		t.Fatalf("leave packets = %d, want 1", len(leaves))
	}
	if leave := readEntityLeave(t, leaves[0]); leave.EntityID != entityID {
		t.Fatalf("leave EntityID = %d, want %d", leave.EntityID, entityID)
	}
	if got := len(readAIStates(t, sink)); got != 0 {
		t.Fatalf("state entries after leaving range = %d, want 0", got)
	}

	sink.Reset()
	s.tickAIReplication(now)
	if got := len(packetsByOpcode(sink, protocol.OpEntityLeaveAoI)); got != 0 {
		t.Fatalf("duplicate leave packets = %d, want 0", got)
	}
}

// OpAIState must contain only in-range squads and chunk at 32 entries/packet.
func TestAI_StateChunkedOnlyOnline(t *testing.T) {
	s, sink := newAIPuppetServer(t, 220.0)
	addAIPlayer(t, s, 9101, 46011, "l01_escape", [3]float32{0, 0, 0})

	const online = 40
	for i := 0; i < online; i++ {
		angle := 2 * math.Pi * float64(i) / float64(online)
		x := float32(50 * math.Cos(angle))
		z := float32(50 * math.Sin(angle))
		if s.registerAIPuppet(ai.PuppetDef{
			Label: "Ring", Section: "sim_default_bandit_0", Faction: "bandit",
			Level: "l01_escape", Spawn: [3]float32{x, 0, z}, PatrolRadius: 5, WalkSpeed: 1.5,
		}) == nil {
			t.Fatal("registerAIPuppet failed")
		}
	}
	// One far puppet must never appear in state or enter/leave traffic.
	far := s.registerAIPuppet(ai.PuppetDef{
		Label: "Far", Section: "sim_default_bandit_1", Faction: "bandit",
		Level: "l01_escape", Spawn: [3]float32{1000, 0, 0}, PatrolRadius: 5, WalkSpeed: 1.5,
	})
	farID, _ := s.AIEntityIDOf(far.ID)

	sink.Reset()
	s.tickAIReplication(time.Now())

	statePackets := packetsByOpcode(sink, protocol.OpAIState)
	wantPackets := (online + protocol.MaxAIStateEntries - 1) / protocol.MaxAIStateEntries
	if len(statePackets) != wantPackets {
		t.Fatalf("AI state packets = %d, want %d", len(statePackets), wantPackets)
	}
	entries := readAIStates(t, sink)
	seen := make(map[uint32]bool, online)
	for _, e := range entries {
		if e.EntityID == farID {
			t.Fatal("far/offline puppet leaked into OpAIState")
		}
		if seen[e.EntityID] {
			t.Fatalf("entity %d delivered more than once", e.EntityID)
		}
		seen[e.EntityID] = true
	}
	if len(entries) != online {
		t.Fatalf("state entries = %d, want %d", len(entries), online)
	}
	if got := len(packetsByOpcode(sink, protocol.OpEntityEnterAoI)); got != online {
		t.Fatalf("enter packets = %d, want %d", got, online)
	}
	if got := len(packetsByOpcode(sink, protocol.OpEntityLeaveAoI)); got != 0 {
		t.Fatalf("unexpected leave packets = %d", got)
	}
}

// Offline squads must macro-step at 1 Hz with a speed*elapsed displacement,
// not once per game tick.
func TestAI_OfflineMacroStepAt1Hz(t *testing.T) {
	s, _ := newAIPuppetServer(t, 100.0)
	addAIPlayer(t, s, 9201, 46021, "l01_escape", [3]float32{5000, 0, 0})

	sq := s.registerAIPuppet(ai.PuppetDef{
		Label: "Macro", Section: "sim_default_stalker_0", Faction: "stalker",
		Level: "l01_escape", Spawn: [3]float32{0, 0, 0}, PatrolRadius: 50, WalkSpeed: 2.0,
	})
	t0 := time.Unix(1_700_000_000, 0)

	s.tickAIReplication(t0) // first sighting: record lastStep, no movement
	if got := aiPuppetPos(s, sq.ID); got != [3]float32{0, 0, 0} {
		t.Fatalf("position after init tick = %v, want origin", got)
	}

	s.tickAIReplication(t0.Add(500 * time.Millisecond)) // < 1 Hz: no step
	if got := aiPuppetPos(s, sq.ID); got != [3]float32{0, 0, 0} {
		t.Fatalf("position after 500ms = %v, want origin", got)
	}

	s.tickAIReplication(t0.Add(time.Second)) // one second elapsed: 2 m/s * 1 s
	got := aiPuppetPos(s, sq.ID)
	if got[0] < 1.99 || got[0] > 2.01 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("position after 1s macro-step = %v, want ~[2 0 0]", got)
	}
}

func aiPuppetPos(s *Server, squadID uint32) [3]float32 {
	for _, p := range s.squads.SnapshotPuppets() {
		if p.ID == squadID {
			return p.Position
		}
	}
	return [3]float32{}
}

// An empty ai_squads table must be seeded with the default l01_escape set, and
// a second server start must not duplicate it.
func TestAI_SeedSquadsWhenTableEmpty(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.RawDB().SetMaxOpenConns(1)
	enabled := true
	cfg := &config.Config{AIEnabled: &enabled}

	s := NewServer(cfg, db, zap.NewNop())
	if got := s.squads.PuppetCount(); got != 4 {
		t.Fatalf("puppet squads after seed = %d, want 4", got)
	}
	var rows int
	if err := db.RawDB().QueryRow("SELECT COUNT(*) FROM ai_squads").Scan(&rows); err != nil {
		t.Fatalf("count ai_squads: %v", err)
	}
	if rows != 4 {
		t.Fatalf("ai_squads rows = %d, want 4", rows)
	}

	// Re-open on the same DB: seed is idempotent, puppets load again.
	s2 := NewServer(cfg, db, zap.NewNop())
	if got := s2.squads.PuppetCount(); got != 4 {
		t.Fatalf("puppet squads after reload = %d, want 4", got)
	}
	if err := db.RawDB().QueryRow("SELECT COUNT(*) FROM ai_squads").Scan(&rows); err != nil {
		t.Fatalf("count ai_squads after reload: %v", err)
	}
	if rows != 4 {
		t.Fatalf("ai_squads rows after reload = %d, want 4", rows)
	}
}

// ai_enabled=false must leave the table unseeded and register zero entities.
func TestAI_DisabledNoEntities(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.RawDB().SetMaxOpenConns(1)
	disabled := false
	s := NewServer(&config.Config{AIEnabled: &disabled}, db, zap.NewNop())
	if got := s.squads.PuppetCount(); got != 0 {
		t.Fatalf("puppet squads with ai_enabled=false = %d, want 0", got)
	}
	var rows int
	if err := db.RawDB().QueryRow("SELECT COUNT(*) FROM ai_squads").Scan(&rows); err != nil {
		t.Fatalf("count ai_squads: %v", err)
	}
	if rows != 0 {
		t.Fatalf("ai_squads rows with ai_enabled=false = %d, want 0", rows)
	}

	sink := &fakeSink{}
	activeSink = sink
	s.udp = sink.UDPListener()
	addAIPlayer(t, s, 9301, 46031, "l01_escape", [3]float32{0, 0, 0})
	sink.Reset()
	s.tickAIReplication(time.Now())
	if got := len(packetsByOpcode(sink, protocol.OpAIState)); got != 0 {
		t.Fatalf("AI packets with ai_enabled=false = %d, want 0", got)
	}
}
