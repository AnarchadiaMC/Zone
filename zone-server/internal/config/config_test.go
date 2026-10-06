package config

import (
	"os"
	"testing"

	"zone-online/zone-server/internal/protocol"
)

func TestLoadConfig(t *testing.T) {
	content := `
port: 27015
tick_rate_hz: 30
max_players: 64
db_path: "test_zone_world.db"
log_level: "info"
emission_interval_min: 45
admin_pipe: "\\\\.\\pipe\\zone_server_admin"
`
	tmpfile, err := os.CreateTemp("", "config_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	tmpfile.Close()

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Port != 27015 {
		t.Errorf("Expected port 27015, got %d", cfg.Port)
	}
	if cfg.TickRateHz != 30 {
		t.Errorf("Expected TickRateHz 30, got %d", cfg.TickRateHz)
	}
	if cfg.MaxPlayers != 64 {
		t.Errorf("Expected MaxPlayers 64, got %d", cfg.MaxPlayers)
	}
	if cfg.DBPath != "test_zone_world.db" {
		t.Errorf("Expected DBPath test_zone_world.db, got %s", cfg.DBPath)
	}
	if cfg.ServerName != "Zone Online" {
		t.Errorf("Expected default ServerName 'Zone Online', got %q", cfg.ServerName)
	}
	if cfg.MapName != "l01_escape" {
		t.Errorf("Expected default MapName 'l01_escape', got %q", cfg.MapName)
	}
	if cfg.GroupMaxPlayers != 4 {
		t.Errorf("Expected default GroupMaxPlayers 4, got %d", cfg.GroupMaxPlayers)
	}
	if cfg.InviteTTLSec != 60 {
		t.Errorf("Expected default InviteTTLSec 60, got %d", cfg.InviteTTLSec)
	}
}

func TestConfigV2Keys(t *testing.T) {
	content := `
port: 27015
tick_rate_hz: 30
max_players: 64
server_name: "Custom Zone"
map_name: "l05_bar_rostok"
mode: 3
locked: true
group_max_players: 6
invite_ttl_sec: 30
`
	tmpfile, err := os.CreateTemp("", "config_v2_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	tmpfile.Close()

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ServerName != "Custom Zone" || cfg.MapName != "l05_bar_rostok" {
		t.Errorf("unexpected server/map: %q/%q", cfg.ServerName, cfg.MapName)
	}
	if cfg.Mode != 3 {
		t.Errorf("Expected Mode 3, got %d", cfg.Mode)
	}
	if !cfg.Locked {
		t.Error("Expected Locked true")
	}
	if cfg.GroupMaxPlayers != 6 {
		t.Errorf("Expected GroupMaxPlayers 6, got %d", cfg.GroupMaxPlayers)
	}
	if cfg.InviteTTLSec != 30 {
		t.Errorf("Expected InviteTTLSec 30, got %d", cfg.InviteTTLSec)
	}
}

// group_max_players is clamped to [1, protocol.MaxGroupMembers], default 4.
func TestGroupMaxPlayersClampedToProtocolCap(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{-5, 4},
		{0, 4},
		{1, 1},
		{4, 4},
		{6, 6},
		{protocol.MaxGroupMembers, protocol.MaxGroupMembers},
		{protocol.MaxGroupMembers + 1, protocol.MaxGroupMembers},
		{100, protocol.MaxGroupMembers},
	}
	for _, tc := range cases {
		cfg := &Config{GroupMaxPlayers: tc.in}
		cfg.SetDefaults()
		if cfg.GroupMaxPlayers != tc.want {
			t.Errorf("GroupMaxPlayers %d clamped to %d, want %d", tc.in, cfg.GroupMaxPlayers, tc.want)
		}
	}
}

// Frozen ledger keys must default even when the YAML omits them.
func TestConfigNetcodeAndLedgerDefaults(t *testing.T) {
	cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: 64}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.DamageBudgetPerS != 400 {
		t.Errorf("DamageBudgetPerS default = %v, want 400", cfg.DamageBudgetPerS)
	}
	if cfg.ItemRatePerS != 5 {
		t.Errorf("ItemRatePerS default = %v, want 5", cfg.ItemRatePerS)
	}
}

func TestConfigNetcodeAndLedgerKeysLoaded(t *testing.T) {
	content := `
port: 27015
tick_rate_hz: 30
max_players: 64
damage_budget_per_s: 275.5
item_rate_per_s: 2.5
`
	tmpfile, err := os.CreateTemp("", "config_netcode_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	tmpfile.Close()

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DamageBudgetPerS != 275.5 || cfg.ItemRatePerS != 2.5 {
		t.Errorf("ledger keys = %v/%v, want 275.5/2.5", cfg.DamageBudgetPerS, cfg.ItemRatePerS)
	}
}

// Validate accepts the supported 48/64 player caps and rejects non-positive
// values, so the handshake full-path can rely on MaxPlayers being sane.
func TestValidate_MaxPlayers48And64(t *testing.T) {
	for _, max := range []int{48, 64} {
		cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: max}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("max_players %d: Validate returned %v, want nil", max, err)
		}
	}

	for _, max := range []int{0, -1} {
		cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: max}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("max_players %d: Validate returned nil, want error", max)
		}
	}
}

// tick_rate_hz must be validated inside 1..240 for both YAML and CLI paths.
func TestValidate_TickRateBounds(t *testing.T) {
	for _, hz := range []int{1, 20, 30, 60, 120, 240} {
		cfg := &Config{Port: 27015, TickRateHz: hz, MaxPlayers: 64}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("tick_rate_hz %d: Validate returned %v, want nil", hz, err)
		}
	}
	for _, hz := range []int{0, -1, 241, 1000} {
		cfg := &Config{Port: 27015, TickRateHz: hz, MaxPlayers: 64}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("tick_rate_hz %d: Validate returned nil, want error", hz)
		}
	}
}

// ai_enabled defaults to true and ai_online_radius_m defaults to 220 when the
// YAML omits both keys.
func TestConfigAIDefaults(t *testing.T) {
	cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: 64}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !cfg.AIEnabledOrDefault() {
		t.Error("AIEnabledOrDefault = false, want true when ai_enabled is omitted")
	}
	if cfg.AIOnlineRadiusM != 220 {
		t.Errorf("AIOnlineRadiusM default = %v, want 220", cfg.AIOnlineRadiusM)
	}
	if cfg.AIEnterRadiusM != 180 {
		t.Errorf("AIEnterRadiusM default = %v, want 180", cfg.AIEnterRadiusM)
	}
	if cfg.AILeaveRadiusM != 220 {
		t.Errorf("AILeaveRadiusM default = %v, want 220", cfg.AILeaveRadiusM)
	}
	if cfg.AIMaxEntities != 64 {
		t.Errorf("AIMaxEntities default = %d, want 64", cfg.AIMaxEntities)
	}
}

// An explicitly tuned legacy ai_online_radius_m pins both hysteresis radii so
// old configs keep their exact boundary.
func TestConfigAILegacyRadiusPinsBoth(t *testing.T) {
	cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: 64, AIOnlineRadiusM: 100}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.AIEnterRadiusM != 100 || cfg.AILeaveRadiusM != 100 {
		t.Errorf("legacy radius aliasing = %v/%v, want 100/100", cfg.AIEnterRadiusM, cfg.AILeaveRadiusM)
	}
}

// Explicit ai keys must override the defaults in both directions.
func TestConfigAIKeysLoaded(t *testing.T) {
	content := `
port: 27015
tick_rate_hz: 30
max_players: 64
ai_enabled: false
ai_online_radius_m: 180.5
`
	tmpfile, err := os.CreateTemp("", "config_ai_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	tmpfile.Close()

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.AIEnabledOrDefault() {
		t.Error("AIEnabledOrDefault = true, want false from explicit ai_enabled: false")
	}
	if cfg.AIOnlineRadiusM != 180.5 {
		t.Errorf("AIOnlineRadiusM = %v, want 180.5", cfg.AIOnlineRadiusM)
	}

	enabled := true
	cfg2 := &Config{AIEnabled: &enabled}
	cfg2.SetDefaults()
	if !cfg2.AIEnabledOrDefault() {
		t.Error("AIEnabledOrDefault = false, want true from explicit ai_enabled: true")
	}
}

// World item lifecycle keys: TTL defaults to 60 minutes (explicit 0 disables
// the sweep), and the per-level cap defaults to 500.
func TestConfigWorldItemDefaultsAndKeys(t *testing.T) {
	cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: 64}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.WorldItemMaxPerLevel != 500 {
		t.Errorf("WorldItemMaxPerLevel default = %d, want 500", cfg.WorldItemMaxPerLevel)
	}
	if got := cfg.WorldItemTTLMinutes(); got != 60 {
		t.Errorf("WorldItemTTLMinutes default = %d, want 60", got)
	}

	zero := 0
	disabled := &Config{WorldItemTTLMin: &zero}
	if got := disabled.WorldItemTTLMinutes(); got != 0 {
		t.Errorf("explicit world_item_ttl_min 0 = %d, want 0 (disabled)", got)
	}

	five := 5
	tuned := &Config{WorldItemTTLMin: &five, WorldItemMaxPerLevel: 12}
	tuned.SetDefaults()
	if got := tuned.WorldItemTTLMinutes(); got != 5 {
		t.Errorf("world_item_ttl_min = %d, want 5", got)
	}
	if tuned.WorldItemMaxPerLevel != 12 {
		t.Errorf("explicit WorldItemMaxPerLevel = %d, want 12", tuned.WorldItemMaxPerLevel)
	}
}

// AI combat and LOS keys default to enabled with the documented numeric
// defaults when omitted.
func TestConfigAICombatAndLOSDefaults(t *testing.T) {
	cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: 64}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !cfg.AICombatEnabledOrDefault() {
		t.Error("AICombatEnabledOrDefault = false, want true when omitted")
	}
	if cfg.AIAggroRadiusM != DefaultAIAggroRadiusM {
		t.Errorf("AIAggroRadiusM = %v, want %v", cfg.AIAggroRadiusM, DefaultAIAggroRadiusM)
	}
	if cfg.AIAttackRangeM != DefaultAIAttackRangeM {
		t.Errorf("AIAttackRangeM = %v, want %v", cfg.AIAttackRangeM, DefaultAIAttackRangeM)
	}
	if cfg.AIAttackCooldownMS != DefaultAIAttackCooldownMS {
		t.Errorf("AIAttackCooldownMS = %d, want %d", cfg.AIAttackCooldownMS, DefaultAIAttackCooldownMS)
	}
	if cfg.AIMeleeDamage != DefaultAIMeleeDamage {
		t.Errorf("AIMeleeDamage = %v, want %v", cfg.AIMeleeDamage, DefaultAIMeleeDamage)
	}
	if cfg.AICorpseSeconds != DefaultAICorpseSeconds {
		t.Errorf("AICorpseSeconds = %d, want %d", cfg.AICorpseSeconds, DefaultAICorpseSeconds)
	}
	if cfg.AIPatrolResumeS != DefaultAIPatrolResumeS {
		t.Errorf("AIPatrolResumeS = %d, want %d", cfg.AIPatrolResumeS, DefaultAIPatrolResumeS)
	}
	if !cfg.LosEnabledOrDefault() {
		t.Error("LosEnabledOrDefault = false, want true when omitted")
	}
	if cfg.LosDataDir != DefaultLOSDataDir {
		t.Errorf("LosDataDir = %q, want %q", cfg.LosDataDir, DefaultLOSDataDir)
	}
}

// Explicit AI combat and LOS keys override defaults in both directions.
func TestConfigAICombatAndLOSKeysLoaded(t *testing.T) {
	content := `
port: 27015
tick_rate_hz: 30
max_players: 64
ai_combat_enabled: false
ai_aggro_radius_m: 25.5
ai_attack_range_m: 1.5
ai_attack_cooldown_ms: 900
ai_melee_damage: 7.5
ai_corpse_seconds: 3
ai_patrol_resume_s: 4
los_enabled: false
los_data_dir: "custom_los"
`
	tmpfile, err := os.CreateTemp("", "config_combat_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	tmpfile.Close()

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.AICombatEnabledOrDefault() {
		t.Error("AICombatEnabledOrDefault = true, want false from explicit key")
	}
	if cfg.AIAggroRadiusM != 25.5 || cfg.AIAttackRangeM != 1.5 {
		t.Errorf("combat radii = %v/%v, want 25.5/1.5", cfg.AIAggroRadiusM, cfg.AIAttackRangeM)
	}
	if cfg.AIAttackCooldownMS != 900 || cfg.AIMeleeDamage != 7.5 {
		t.Errorf("cooldown/damage = %d/%v, want 900/7.5", cfg.AIAttackCooldownMS, cfg.AIMeleeDamage)
	}
	if cfg.AICorpseSeconds != 3 || cfg.AIPatrolResumeS != 4 {
		t.Errorf("corpse/resume = %d/%d, want 3/4", cfg.AICorpseSeconds, cfg.AIPatrolResumeS)
	}
	if cfg.LosEnabledOrDefault() {
		t.Error("LosEnabledOrDefault = true, want false from explicit key")
	}
	if cfg.LOSDataDirOrDefault() != "custom_los" {
		t.Errorf("LOSDataDirOrDefault = %q, want custom_los", cfg.LOSDataDirOrDefault())
	}
}

// trade_max_money_delta defaults to 200000 and clamps non-positive values;
// explicit YAML values override it.
func TestConfigTradeMaxMoneyDelta(t *testing.T) {
	cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: 64}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.TradeMaxMoneyDelta != DefaultTradeMaxMoneyDelta {
		t.Errorf("TradeMaxMoneyDelta default = %d, want %d", cfg.TradeMaxMoneyDelta, DefaultTradeMaxMoneyDelta)
	}
	if got := cfg.TradeMaxMoneyDeltaOrDefault(); got != 200000 {
		t.Errorf("TradeMaxMoneyDeltaOrDefault = %d, want 200000", got)
	}

	zero := &Config{}
	if got := zero.TradeMaxMoneyDeltaOrDefault(); got != DefaultTradeMaxMoneyDelta {
		t.Errorf("nil-key TradeMaxMoneyDeltaOrDefault = %d, want %d", got, DefaultTradeMaxMoneyDelta)
	}
	zero.SetDefaults()
	if zero.TradeMaxMoneyDelta != DefaultTradeMaxMoneyDelta {
		t.Errorf("TradeMaxMoneyDelta after SetDefaults = %d, want %d", zero.TradeMaxMoneyDelta, DefaultTradeMaxMoneyDelta)
	}

	content := `
port: 27015
tick_rate_hz: 30
max_players: 64
trade_max_money_delta: 5000
`
	tmpfile, err := os.CreateTemp("", "config_trade_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	tmpfile.Close()

	loaded, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.TradeMaxMoneyDelta != 5000 {
		t.Errorf("trade_max_money_delta = %d, want 5000", loaded.TradeMaxMoneyDelta)
	}
}
