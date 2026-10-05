package game

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// findOpcodePacket returns the most recent recorded packet with the given
// opcode, or nil when none was recorded.
func findOpcodePacket(t *testing.T, sink *fakeSink, op uint16) []byte {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for i := len(sink.sent) - 1; i >= 0; i-- {
		if len(sink.sent[i]) < 12 {
			continue
		}
		hdr, err := protocol.ReadHeader(bytes.NewReader(sink.sent[i][:12]))
		if err != nil {
			continue
		}
		if hdr.Opcode == op {
			return sink.sent[i]
		}
	}
	return nil
}

func parseSafezoneState(t *testing.T, raw []byte) protocol.SafezoneStatePayload {
	t.Helper()
	if raw == nil {
		t.Fatal("expected OpSafezoneState packet, got none")
	}
	r := bytes.NewReader(raw)
	if _, err := protocol.ReadHeader(r); err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}
	var sz protocol.SafezoneStatePayload
	if err := binary.Read(r, binary.LittleEndian, &sz); err != nil {
		t.Fatalf("read SafezoneStatePayload failed: %v", err)
	}
	return sz
}

func TestProtocolV2_InitialSafezoneStateOnJoin(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45001}
	var req protocol.HandshakeReq
	copy(req.UUID[:], "uuid-v2-initial-safezone")
	copy(req.Nickname[:], "NewStalker")
	req.ProtocolVer = protocol.ProtocolVer

	s.HandlePacket(buildTestPacket(t, protocol.OpHandshakeReq, 1, protocol.FlagReliable, req), addr)

	// Default spawn is inside sz_cordon_rookie, so the immediate snapshot
	// must be locked=1 with the zone ID.
	sz := parseSafezoneState(t, findOpcodePacket(t, sink, protocol.OpSafezoneState))
	if sz.Locked != 1 {
		t.Fatalf("expected initial Locked 1 inside spawn zone, got %d", sz.Locked)
	}
	zoneID := string(bytes.TrimRight(sz.ZoneID[:], "\x00"))
	if zoneID != "sz_cordon_rookie" {
		t.Fatalf("expected zone sz_cordon_rookie, got %q", zoneID)
	}

	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatal("expected session after handshake")
	}
	sess.Lock()
	inSafe := sess.InSafeZone
	sess.Unlock()
	if !inSafe {
		t.Fatal("expected session InSafeZone after initial snapshot")
	}
}

func TestProtocolV2_LevelChangeUpdatesLevelAndResendsState(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45002}
	sess := &network.PlayerSession{
		SessionID:    7101,
		AccountID:    "uuid-v2-level",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{-211.3, -20.2, -145.8},
		Health:       100.0,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)

	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], "l02_garbage")
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 1, protocol.FlagReliable, lvl), addr)

	sess.Lock()
	level := sess.CurrentLevel
	inSafe := sess.InSafeZone
	sess.Unlock()
	if level != "l02_garbage" {
		t.Fatalf("expected level l02_garbage, got %q", level)
	}
	if inSafe {
		t.Fatal("expected safe-zone flag reset on level change")
	}

	sz := parseSafezoneState(t, findOpcodePacket(t, sink, protocol.OpSafezoneState))
	if sz.Locked != 0 {
		t.Fatalf("expected Locked 0 after moving to a zone-free level, got %d", sz.Locked)
	}
}

func TestProtocolV2_LevelChangeRejectsInvalidName(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45003}
	sess := &network.PlayerSession{
		SessionID:    7102,
		AccountID:    "uuid-v2-bad-level",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)

	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], "this_level_name_is_far_too_long_to_accept")
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 1, protocol.FlagReliable, lvl), addr)

	sess.Lock()
	level := sess.CurrentLevel
	sess.Unlock()
	if level != "l01_escape" {
		t.Fatalf("invalid level name was accepted: %q", level)
	}
}

func TestProtocolV2_ServerQueryResponseFields(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	s.cfg = &config.Config{
		ServerName: "Test Zone",
		MapName:    "l02_garbage",
		Mode:       2,
		Locked:     true,
		MaxPlayers: 48,
		TickRateHz: 60,
	}
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45004}
	s.HandlePacket(buildTestPacket(t, protocol.OpServerQuery, 1, protocol.FlagUnreliable, nil), addr)

	if s.sessions.GetByAddr(addr.String()) != nil {
		t.Fatal("OpServerQuery must not create a session")
	}

	raw := findOpcodePacket(t, sink, protocol.OpServerQueryRes)
	if raw == nil {
		t.Fatal("expected OpServerQueryRes, got none")
	}
	r := bytes.NewReader(raw)
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}
	if hdr.PayloadLength != 70 {
		t.Fatalf("expected 70-byte payload, got %d", hdr.PayloadLength)
	}
	res, err := protocol.ReadServerQueryRes(r)
	if err != nil {
		t.Fatalf("ReadServerQueryRes failed: %v", err)
	}
	if got := string(bytes.TrimRight(res.Name[:], "\x00")); got != "Test Zone" {
		t.Errorf("Name = %q, want Test Zone", got)
	}
	if got := string(bytes.TrimRight(res.Map[:], "\x00")); got != "l02_garbage" {
		t.Errorf("Map = %q, want l02_garbage", got)
	}
	if res.Players != 0 {
		t.Errorf("Players = %d, want 0", res.Players)
	}
	if res.MaxPlayers != 48 {
		t.Errorf("MaxPlayers = %d, want 48", res.MaxPlayers)
	}
	if res.Mode != 2 {
		t.Errorf("Mode = %d, want 2", res.Mode)
	}
	if res.Locked != 1 {
		t.Errorf("Locked = %d, want 1", res.Locked)
	}
	if res.ProtoVer != protocol.ProtocolVer {
		t.Errorf("ProtoVer = %d, want %d", res.ProtoVer, protocol.ProtocolVer)
	}
	if res.TickRateHz != 60 {
		t.Errorf("TickRateHz = %d, want 60", res.TickRateHz)
	}
}

func TestProtocolV2_DamageRejectedWhenAttackerInSafeZone(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	attackerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45005}
	attacker := &network.PlayerSession{
		SessionID:    7201,
		AccountID:    "uuid-v2-attacker",
		UDPAddr:      attackerAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{-211.3, -20.2, -145.8},
		Health:       100.0,
		InSafeZone:   true,
		LastSeen:     time.Now(),
	}
	target := &network.PlayerSession{
		SessionID:    7202,
		AccountID:    "uuid-v2-target",
		UDPAddr:      &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45006},
		CurrentLevel: "l01_escape",
		Position:     [3]float32{-208.0, -20.2, -145.8},
		Health:       100.0,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(attacker)
	s.sessions.AddSession(target)

	dmg := protocol.DamageNotify{TargetID: 7202, AttackerID: 7201, Damage: 25.0, BoneID: 1}
	s.HandlePacket(buildTestPacket(t, protocol.OpDamageNotify, 1, protocol.FlagReliable, dmg), attackerAddr)

	target.Lock()
	health := target.Health
	target.Unlock()
	if health != 100.0 {
		t.Fatalf("attacker in safe zone dealt damage: target health %f, want 100", health)
	}
}

func TestProtocolV2_SleeperDamageUsesSameValidation(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	victim := &network.PlayerSession{
		SessionID:    7301,
		AccountID:    "uuid-v2-sleeper",
		CurrentLevel: "l01_escape",
		Position:     [3]float32{100.0, 0.0, 100.0},
		Health:       80.0,
	}
	sleeper := s.sleepers.CreateSleeper(victim, time.Minute)
	if sleeper == nil {
		t.Fatal("failed to create sleeper")
	}

	attackerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45007}
	attacker := &network.PlayerSession{
		SessionID:    7302,
		AccountID:    "uuid-v2-sleeper-attacker",
		UDPAddr:      attackerAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{101.0, 0.0, 100.0},
		Health:       100.0,
		InSafeZone:   true,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(attacker)

	dmg := protocol.DamageNotify{TargetID: sleeper.EntityID, AttackerID: attacker.SessionID, Damage: 30.0, BoneID: 1}
	s.HandlePacket(buildTestPacket(t, protocol.OpDamageNotify, 1, protocol.FlagReliable, dmg), attackerAddr)

	if got := sleeper.Health; got != 80.0 {
		t.Fatalf("sleeper took damage despite attacker safe-zone immunity: health %f, want 80", got)
	}

	// Same attacker, now outside the safe zone: damage must go through.
	attacker.Lock()
	attacker.InSafeZone = false
	attacker.Unlock()
	s.HandlePacket(buildTestPacket(t, protocol.OpDamageNotify, 2, protocol.FlagReliable, dmg), attackerAddr)

	if got := sleeper.Health; got != 50.0 {
		t.Fatalf("sleeper health %f after valid 30 damage, want 50", got)
	}
}

func entityEnterPacketsFor(t *testing.T, sink *fakeSink, entityID uint32) []protocol.EntityEnterAoI {
	t.Helper()
	var out []protocol.EntityEnterAoI
	for _, raw := range packetsByOpcode(sink, protocol.OpEntityEnterAoI) {
		r := bytes.NewReader(raw)
		if _, err := protocol.ReadHeader(r); err != nil {
			continue
		}
		var enter protocol.EntityEnterAoI
		if err := binary.Read(r, binary.LittleEndian, &enter); err != nil {
			continue
		}
		if enter.EntityID == entityID {
			out = append(out, enter)
		}
	}
	return out
}

func TestProtocolV2_EntityEnterWaitsForGvidAndVisual(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45010}
	moverAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45011}
	peer := &network.PlayerSession{
		SessionID:    7501,
		AccountID:    "uuid-enter-peer",
		UDPAddr:      peerAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{1, 0, 1},
		Health:       100,
		HasVisual:    true,
		Gvid:         100,
		LastSeen:     time.Now(),
	}
	mover := &network.PlayerSession{
		SessionID:    7502,
		AccountID:    "uuid-enter-mover",
		UDPAddr:      moverAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{0, 0, 0},
		Health:       100,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(peer)
	s.sessions.AddSession(mover)
	sink.Reset()

	ctZero := protocol.ClientTransform{PosX: 0, PosY: 0, PosZ: 0}
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 100, protocol.FlagUnreliable, ctZero), moverAddr)
	if got := len(entityEnterPacketsFor(t, sink, mover.SessionID)); got != 0 {
		t.Fatalf("ENTITY_ENTER broadcast for gvid 0: got %d, want 0", got)
	}

	ctGvid := protocol.ClientTransform{PosX: 0, PosY: 0, PosZ: 0, Gvid: 0x0123}
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 101, protocol.FlagUnreliable, ctGvid), moverAddr)
	if got := len(entityEnterPacketsFor(t, sink, mover.SessionID)); got != 0 {
		t.Fatalf("ENTITY_ENTER broadcast before visual known: got %d, want 0", got)
	}

	var pv protocol.PlayerVisualPayload
	copy(pv.Visual[:], `actors\stalker_neutral\stalker_neutral_1`)
	s.HandlePacket(buildTestPacket(t, protocol.OpPlayerVisual, 200, protocol.FlagReliable, pv), moverAddr)

	enters := entityEnterPacketsFor(t, sink, mover.SessionID)
	if len(enters) != 1 {
		t.Fatalf("expected exactly 1 ENTITY_ENTER once gvid and visual known, got %d", len(enters))
	}
	if enters[0].Gvid != 0x0123 {
		t.Errorf("ENTITY_ENTER gvid = 0x%04X, want 0x0123", enters[0].Gvid)
	}
	if got := string(bytes.TrimRight(enters[0].Section[:], "\x00")); got != `actors\stalker_neutral\stalker_neutral_1` {
		t.Errorf("ENTITY_ENTER section = %q", got)
	}

	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 300, protocol.FlagUnreliable, ctGvid), moverAddr)
	if got := len(entityEnterPacketsFor(t, sink, mover.SessionID)); got != 1 {
		t.Fatalf("ENTITY_ENTER re-broadcast for same level: got %d, want 1", got)
	}
}

func TestProtocolV2_LevelChangeResetsAndRebroadcastsEntityEnter(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	peerOldAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45012}
	peerNewAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45013}
	moverAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45014}
	peerOld := &network.PlayerSession{
		SessionID:    7601,
		AccountID:    "uuid-lvl-peer-old",
		UDPAddr:      peerOldAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{1, 0, 1},
		Health:       100,
		HasVisual:    true,
		Gvid:         10,
		LastSeen:     time.Now(),
	}
	peerNew := &network.PlayerSession{
		SessionID:    7602,
		AccountID:    "uuid-lvl-peer-new",
		UDPAddr:      peerNewAddr,
		CurrentLevel: "l02_garbage",
		Position:     [3]float32{2, 0, 2},
		Health:       100,
		HasVisual:    true,
		Gvid:         20,
		LastSeen:     time.Now(),
	}
	mover := &network.PlayerSession{
		SessionID:    7603,
		AccountID:    "uuid-lvl-mover",
		UDPAddr:      moverAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{0, 0, 0},
		Health:       100,
		HasVisual:    true,
		Gvid:         0x1111,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(peerOld)
	s.sessions.AddSession(peerNew)
	s.sessions.AddSession(mover)
	sink.Reset()

	ctOld := protocol.ClientTransform{PosX: 0, PosY: 0, PosZ: 0, Gvid: 0x1111}
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 100, protocol.FlagUnreliable, ctOld), moverAddr)
	if got := len(entityEnterPacketsFor(t, sink, mover.SessionID)); got != 1 {
		t.Fatalf("expected 1 initial ENTITY_ENTER, got %d", got)
	}
	sink.Reset()

	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], "l02_garbage")
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 200, protocol.FlagReliable, lvl), moverAddr)

	if got := len(entityEnterPacketsFor(t, sink, mover.SessionID)); got != 0 {
		t.Fatalf("ENTITY_ENTER broadcast immediately after level change: got %d, want 0", got)
	}
	if got := len(packetsByOpcode(sink, protocol.OpEntityLeaveAoI)); got != 1 {
		t.Fatalf("expected 1 ENTITY_LEAVE for the old level, got %d", got)
	}

	// The level change reset Position to the server-known spawn; the client's
	// first post-load transform arrives at that spawn.
	ctNew := protocol.ClientTransform{
		PosX: defaultLevelSpawn[0], PosY: defaultLevelSpawn[1], PosZ: defaultLevelSpawn[2],
		Gvid: 0x2222,
	}
	s.HandlePacket(buildTestPacket(t, protocol.OpClientTransform, 300, protocol.FlagUnreliable, ctNew), moverAddr)
	enters := entityEnterPacketsFor(t, sink, mover.SessionID)
	if len(enters) != 1 {
		t.Fatalf("expected exactly 1 ENTITY_ENTER after level change + gvid, got %d", len(enters))
	}
	if enters[0].Gvid != 0x2222 {
		t.Errorf("re-broadcast gvid = 0x%04X, want 0x2222", enters[0].Gvid)
	}
}

func TestProtocolV2_PlayerVisualBroadcastsEntityEnter(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addrA := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45008}
	addrB := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45009}
	sessA := &network.PlayerSession{
		SessionID:    7401,
		AccountID:    "uuid-v2-visual-a",
		UDPAddr:      addrA,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{1.0, 0.0, 1.0},
		Faction:      "dolg",
		Health:       90.0,
		Gvid:         0x0AAA,
		LastSeen:     time.Now(),
	}
	sessB := &network.PlayerSession{
		SessionID:    7402,
		AccountID:    "uuid-v2-visual-b",
		UDPAddr:      addrB,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{2.0, 0.0, 2.0},
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sessA)
	s.sessions.AddSession(sessB)

	var pv protocol.PlayerVisualPayload
	copy(pv.Visual[:], `actors\stalker_neutral\stalker_neutral_2`)
	s.HandlePacket(buildTestPacket(t, protocol.OpPlayerVisual, 1, protocol.FlagReliable, pv), addrA)

	raw := findOpcodePacket(t, sink, protocol.OpEntityEnterAoI)
	if raw == nil {
		t.Fatal("expected ENTITY_ENTER_AOI broadcast after OpPlayerVisual")
	}
	r := bytes.NewReader(raw)
	if _, err := protocol.ReadHeader(r); err != nil {
		t.Fatalf("ReadHeader failed: %v", err)
	}
	var enter protocol.EntityEnterAoI
	if err := binary.Read(r, binary.LittleEndian, &enter); err != nil {
		t.Fatalf("read EntityEnterAoI failed: %v", err)
	}
	if enter.EntityID != sessA.SessionID || enter.EntityType != 1 {
		t.Errorf("enter identity mismatch: %+v", enter)
	}
	if got := string(bytes.TrimRight(enter.Section[:], "\x00")); got != `actors\stalker_neutral\stalker_neutral_2` {
		t.Errorf("enter section = %q", got)
	}
	if got := string(bytes.TrimRight(enter.Faction[:], "\x00")); got != "dolg" {
		t.Errorf("enter faction = %q, want dolg", got)
	}
	if enter.Health != 90 || enter.Gvid != 0x0AAA {
		t.Errorf("enter health/gvid = %d/0x%04X, want 90/0x0AAA", enter.Health, enter.Gvid)
	}

	sessA.Lock()
	hasVisual := sessA.HasVisual
	sessA.Unlock()
	if !hasVisual {
		t.Error("expected session visual to be stored")
	}
}
