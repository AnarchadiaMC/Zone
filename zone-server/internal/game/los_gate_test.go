package game

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/los"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// writeWallOccluder builds a wall plane at x=5 spanning z in [-10, 10] and
// y in [minY, maxY], saving it as <dir>/<level>.occl.
func writeWallOccluder(t *testing.T, dir, level string, minY, maxY float32) {
	t.Helper()
	mesh := &los.Mesh{
		Vertices: [][3]float32{
			{5, minY, -10},
			{5, minY, 10},
			{5, maxY, 10},
			{5, maxY, -10},
		},
		Triangles: [][3]uint32{{0, 1, 2}, {0, 2, 3}},
	}
	occ, err := los.Build(mesh, los.Options{CellSize: 1})
	if err != nil {
		t.Fatalf("los.Build: %v", err)
	}
	if err := occ.Save(filepath.Join(dir, level+".occl")); err != nil {
		t.Fatalf("occluder save: %v", err)
	}
}

// wireLOS swaps the server's LOS config and checker (the test helper builds the
// server with nil config, so defaults would need a real zone_los dir).
func wireLOS(s *Server, dir string, enabled bool) {
	s.losMgr = newLOSManager(enabled, dir, zap.NewNop())
	s.damageHandler.SetLOSChecker(s.losMgr.damageAllowed)
}

func losAddr(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
}

// losSessions inserts an attacker at (0,0,0) and a target 10 m away on the
// given level, both outside safe zones.
func losSessions(t *testing.T, s *Server, level string) (*network.PlayerSession, *network.PlayerSession) {
	t.Helper()
	attacker := &network.PlayerSession{
		SessionID:    9501,
		AccountID:    "uuid-los-attacker",
		UDPAddr:      losAddr(39001),
		CurrentLevel: level,
		Faction:      "stalker",
		Position:     [3]float32{0, 0, 0},
		Health:       100,
	}
	target := &network.PlayerSession{
		SessionID:    9502,
		AccountID:    "uuid-los-target",
		UDPAddr:      losAddr(39002),
		CurrentLevel: level,
		Faction:      "bandit",
		Position:     [3]float32{10, 0, 0},
		Health:       100,
	}
	s.sessions.AddSession(attacker)
	s.sessions.AddSession(target)
	return attacker, target
}

// A tall wall blocks all three samples (eye->head/chest/pelvis): the hit is
// rejected with the audit reason "no line of sight".
func TestLOS_TallWallRejectsHit(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	dir := t.TempDir()
	writeWallOccluder(t, dir, "l01_escape", -1, 3)
	wireLOS(s, dir, true)
	attacker, target := losSessions(t, s, "l01_escape")
	sink.Reset()

	sendDamageNotify(t, s, attacker, 1, target.SessionID, 30)
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 0 {
		t.Fatalf("blocked hit relayed %d packets, want 0", got)
	}
	target.Lock()
	health := target.Health
	target.Unlock()
	if health != 100 {
		t.Fatalf("blocked hit changed health to %v, want 100", health)
	}

	var reason string
	if err := db.RawDB().QueryRow(
		"SELECT detail FROM audit_log WHERE event_type='damage_rejected' ORDER BY id DESC LIMIT 1").Scan(&reason); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if !bytes.Contains([]byte(reason), []byte("no line of sight")) {
		t.Fatalf("audit reason = %q, want no line of sight", reason)
	}
}

// A low wall blocks only the pelvis sample: head/chest clear, so the hit lands.
func TestLOS_LowWallAllowsChestSample(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	dir := t.TempDir()
	writeWallOccluder(t, dir, "l01_escape", -1, 1.2)
	wireLOS(s, dir, true)
	attacker, target := losSessions(t, s, "l01_escape")
	sink.Reset()

	sendDamageNotify(t, s, attacker, 1, target.SessionID, 30)
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 1 {
		t.Fatalf("clear hit relays = %d, want 1", got)
	}
	target.Lock()
	health := target.Health
	target.Unlock()
	if health != 70 {
		t.Fatalf("target health after 30 damage = %v, want 70", health)
	}
}

// Missing, corrupt and unsafe-named occluders fail OPEN: the hit lands and no
// panic/traversal happens.
func TestLOS_FailsOpen(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	dir := t.TempDir()

	// Missing file for l01_escape.
	wireLOS(s, dir, true)
	attacker, target := losSessions(t, s, "l01_escape")
	sink.Reset()
	sendDamageNotify(t, s, attacker, 1, target.SessionID, 20)
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 1 {
		t.Fatalf("missing occluder blocked damage: relays %d, want 1", got)
	}

	// Corrupt file for l02_garbage: two hits, no panic, damage lands.
	if err := os.WriteFile(filepath.Join(dir, "l02_garbage.occl"), []byte("not an occluder"), 0o644); err != nil {
		t.Fatal(err)
	}
	wireLOS(s, dir, true)
	attacker2, target2 := losSessions(t, s, "l02_garbage")
	sink.Reset()
	sendDamageNotify(t, s, attacker2, 1, target2.SessionID, 20)
	sendDamageNotify(t, s, attacker2, 2, target2.SessionID, 20)
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 2 {
		t.Fatalf("corrupt occluder blocked damage: relays %d, want 2", got)
	}

	// Unsafe level name: never used as a path component; fail open.
	attacker3, target3 := losSessions(t, s, "../..")
	sink.Reset()
	sendDamageNotify(t, s, attacker3, 1, target3.SessionID, 20)
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 1 {
		t.Fatalf("unsafe level name blocked damage: relays %d, want 1", got)
	}
}

// los_enabled=false ignores available occluders entirely.
func TestLOS_DisabledAllowsDamage(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	dir := t.TempDir()
	writeWallOccluder(t, dir, "l01_escape", -1, 3)
	wireLOS(s, dir, false)
	attacker, target := losSessions(t, s, "l01_escape")
	sink.Reset()

	sendDamageNotify(t, s, attacker, 1, target.SessionID, 25)
	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 1 {
		t.Fatalf("los_enabled=false blocked damage: relays %d, want 1", got)
	}
}

// The negative occluder cache is bounded: client-reported level names beyond
// the cap still fail open but are not retained, so the key space cannot grow
// without bound.
func TestLOS_MissingCacheBounded(t *testing.T) {
	m := newLOSManager(true, t.TempDir(), zap.NewNop())
	for i := 0; i < losMaxNegativeCacheEntries+64; i++ {
		level := "unknown_level_" + strconv.Itoa(i)
		if !m.damageAllowed(level, [3]float32{0, 0, 0}, [3]float32{1, 1, 1}) {
			t.Fatalf("missing occluder for %s blocked damage, want fail open", level)
		}
	}
	if len(m.missing) > losMaxNegativeCacheEntries {
		t.Fatalf("negative occluder cache grew to %d entries, want <= %d",
			len(m.missing), losMaxNegativeCacheEntries)
	}
}

// Default config keeps LOS enabled with the documented data dir.
func TestLOS_ConfigDefaults(t *testing.T) {
	cfg := &config.Config{}
	if !cfg.LosEnabledOrDefault() {
		t.Fatal("LosEnabledOrDefault = false, want true")
	}
	if got := cfg.LOSDataDirOrDefault(); got != config.DefaultLOSDataDir {
		t.Fatalf("LOSDataDirOrDefault = %q, want %q", got, config.DefaultLOSDataDir)
	}
}
