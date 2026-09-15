package game

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

func TestLifecycle_HandshakeSuccess(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30001}
	var req protocol.HandshakeReq
	copy(req.UUID[:], "uuid-lifecycle-hero-1")
	copy(req.Nickname[:], "MajorDegtyarev")
	req.ProtocolVer = 1

	raw := buildTestPacket(t, protocol.OpHandshakeReq, 1, protocol.FlagReliable, req)
	s.HandlePacket(raw, addr)

	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatalf("expected session to be added to sessions map for addr %s", addr.String())
	}
	sess.Lock()
	sessID := sess.SessionID
	accID := sess.AccountID
	pos := sess.Position
	health := sess.Health
	level := sess.CurrentLevel
	sess.Unlock()

	if sessID == 0 {
		t.Fatal("expected non-zero valid session ID")
	}
	if accID != "uuid-lifecycle-hero-1" {
		t.Errorf("expected AccountID 'uuid-lifecycle-hero-1', got '%s'", accID)
	}
	if health <= 0 {
		t.Errorf("expected positive health, got %f", health)
	}
	if level != "l01_escape" {
		t.Errorf("expected default level 'l01_escape', got '%s'", level)
	}

	// Verify session added to spatial grid
	if !s.grid.Contains(sessID) {
		t.Errorf("expected session %d to be inserted into spatial grid", sessID)
	}
	gx, gz, ok := s.grid.GetPosition(sessID)
	if !ok || gx != pos[0] || gz != pos[2] {
		t.Errorf("expected grid coordinates (%f, %f), got (%f, %f)", pos[0], pos[2], gx, gz)
	}

	// Verify Handshake response
	if sink.PacketCount() == 0 {
		t.Fatal("expected OpHandshakeRes packet sent, got none")
	}

	r := bytes.NewReader(sink.LastPacket())
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("failed to read packet header: %v", err)
	}
	if hdr.Opcode != protocol.OpHandshakeRes {
		t.Fatalf("expected Opcode OpHandshakeRes (0x0002), got 0x%04X", hdr.Opcode)
	}

	var res protocol.HandshakeRes
	if err := binary.Read(r, binary.LittleEndian, &res); err != nil {
		t.Fatalf("failed to read HandshakeRes: %v", err)
	}
	resSessID := binary.LittleEndian.Uint32(res.SessionID[:])
	if resSessID != sessID {
		t.Errorf("expected HandshakeRes SessionID %d, got %d", sessID, resSessID)
	}
	if res.Status != 0 {
		t.Errorf("expected HandshakeRes Status 0 (success), got %d", res.Status)
	}
	if res.SpawnX != pos[0] || res.SpawnY != pos[1] || res.SpawnZ != pos[2] {
		t.Errorf("expected spawn pos %v, got [%f, %f, %f]", pos, res.SpawnX, res.SpawnY, res.SpawnZ)
	}
	if res.EcoTier != 1 {
		t.Errorf("expected EcoTier 1, got %d", res.EcoTier)
	}
	if res.WorldTime == 0 {
		t.Errorf("expected non-zero WorldTime")
	}
}

func TestLifecycle_HandshakeMaxPlayersEnforcement(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	s.cfg = &config.Config{
		MaxPlayers: 1,
	}

	// Connect 1st player (must succeed)
	addr1 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30011}
	var req1 protocol.HandshakeReq
	copy(req1.UUID[:], "uuid-player-first")
	copy(req1.Nickname[:], "PlayerOne")
	req1.ProtocolVer = protocol.ProtocolVer
	raw1 := buildTestPacket(t, protocol.OpHandshakeReq, 1, protocol.FlagReliable, req1)
	s.HandlePacket(raw1, addr1)

	if s.sessions.GetByAddr(addr1.String()) == nil {
		t.Fatalf("player 1 failed to connect under max_players=1")
	}
	if sink.PacketCount() == 0 {
		t.Fatalf("expected HandshakeRes for player 1")
	}
	r1 := bytes.NewReader(sink.LastPacket())
	_, _ = protocol.ReadHeader(r1)
	var res1 protocol.HandshakeRes
	_ = binary.Read(r1, binary.LittleEndian, &res1)
	if res1.Status != 0 {
		t.Errorf("expected player 1 Status 0, got %d", res1.Status)
	}

	sink.Reset()

	// Connect 2nd player (must be rejected with Status 1)
	addr2 := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30012}
	var req2 protocol.HandshakeReq
	copy(req2.UUID[:], "uuid-player-second")
	copy(req2.Nickname[:], "PlayerTwo")
	req2.ProtocolVer = protocol.ProtocolVer
	raw2 := buildTestPacket(t, protocol.OpHandshakeReq, 2, protocol.FlagReliable, req2)
	s.HandlePacket(raw2, addr2)

	// Verify player 2 was not added
	if s.sessions.GetByAddr(addr2.String()) != nil {
		t.Fatalf("player 2 should not have been added to sessions map when server is full")
	}
	if sink.PacketCount() == 0 {
		t.Fatalf("expected HandshakeRes rejection packet sent to player 2")
	}

	r2 := bytes.NewReader(sink.LastPacket())
	hdr2, err := protocol.ReadHeader(r2)
	if err != nil {
		t.Fatalf("failed to read header for player 2 response: %v", err)
	}
	if hdr2.Opcode != protocol.OpHandshakeRes {
		t.Fatalf("expected Opcode OpHandshakeRes, got 0x%04X", hdr2.Opcode)
	}
	var res2 protocol.HandshakeRes
	if err := binary.Read(r2, binary.LittleEndian, &res2); err != nil {
		t.Fatalf("failed to read HandshakeRes for player 2: %v", err)
	}
	if res2.Status != 1 {
		t.Errorf("expected player 2 rejection Status 1, got %d", res2.Status)
	}
}

func TestLifecycle_ClientTransformSafeZoneTransition(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30021}
	sessID := uint32(501)
	sess := &network.PlayerSession{
		SessionID:    sessID,
		AccountID:    "uuid-safezone-test",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{0, 0, 0},
		Health:       100.0,
		InSafeZone:   false,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)
	s.grid.Insert(sessID, 0, 0)

	// 1. Transform outside safe zone (0, 0, 0 on l01_escape is outside)
	ctOut := protocol.ClientTransform{
		PosX: 0.0,
		PosY: 0.0,
		PosZ: 0.0,
	}
	rawOut := buildTestPacket(t, protocol.OpClientTransform, 1, protocol.FlagUnreliable, ctOut)
	s.HandlePacket(rawOut, addr)

	sess.Lock()
	inSZ := sess.InSafeZone
	sess.Unlock()
	if inSZ {
		t.Errorf("expected InSafeZone to be false at (0, 0, 0)")
	}
	sink.Reset()

	// 2. Transform into Cordon Rookie Village: (-211.3, -20.2, -145.8, "l01_escape")
	sess.Lock()
	sess.LastTransformTime = time.Time{}
	sess.Unlock()
	ctIn := protocol.ClientTransform{
		PosX: -211.3,
		PosY: -20.2,
		PosZ: -145.8,
	}
	rawIn := buildTestPacket(t, protocol.OpClientTransform, 2, protocol.FlagUnreliable, ctIn)
	s.HandlePacket(rawIn, addr)

	sess.Lock()
	inSZ = sess.InSafeZone
	sess.Unlock()
	if !inSZ {
		t.Fatalf("expected InSafeZone to be true after moving into Cordon Rookie Village")
	}

	// Verify OpSafezoneState packet was sent
	if sink.PacketCount() == 0 {
		t.Fatalf("expected OpSafezoneState packet to be sent on entering safe zone")
	}

	r := bytes.NewReader(sink.LastPacket())
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("failed to read header: %v", err)
	}
	if hdr.Opcode != protocol.OpSafezoneState {
		t.Fatalf("expected Opcode OpSafezoneState (0x0020), got 0x%04X", hdr.Opcode)
	}

	var sz protocol.SafezoneStatePayload
	if err := binary.Read(r, binary.LittleEndian, &sz); err != nil {
		t.Fatalf("failed to read SafezoneState payload: %v", err)
	}
	if sz.Locked != 1 {
		t.Errorf("expected Locked 1, got %d", sz.Locked)
	}
	zid := string(bytes.Trim(sz.ZoneID[:], "\x00"))
	if zid != "sz_cordon_rookie" {
		t.Errorf("expected ZoneID 'sz_cordon_rookie', got '%s'", zid)
	}

	// Verify spatial grid was also updated to new coordinates
	gx, gz, ok := s.grid.GetPosition(sessID)
	if !ok || gx != -211.3 || gz != -145.8 {
		t.Errorf("expected spatial grid position (-211.3, -145.8), got (%f, %f)", gx, gz)
	}

	sink.Reset()

	// 3. Move back outside Rookie Village
	sess.Lock()
	sess.LastTransformTime = time.Time{}
	sess.Unlock()
	ctOut2 := protocol.ClientTransform{
		PosX: 50.0,
		PosY: 0.0,
		PosZ: 50.0,
	}
	rawOut2 := buildTestPacket(t, protocol.OpClientTransform, 3, protocol.FlagUnreliable, ctOut2)
	s.HandlePacket(rawOut2, addr)

	sess.Lock()
	inSZ = sess.InSafeZone
	sess.Unlock()
	if inSZ {
		t.Fatalf("expected InSafeZone to be false after exiting Cordon Rookie Village")
	}

	if sink.PacketCount() == 0 {
		t.Fatalf("expected OpSafezoneState packet on exiting safe zone")
	}
	rOut := bytes.NewReader(sink.LastPacket())
	hdrOut, _ := protocol.ReadHeader(rOut)
	if hdrOut.Opcode != protocol.OpSafezoneState {
		t.Fatalf("expected Opcode OpSafezoneState, got 0x%04X", hdrOut.Opcode)
	}
	var szOut protocol.SafezoneStatePayload
	_ = binary.Read(rOut, binary.LittleEndian, &szOut)
	if szOut.Locked != 0 {
		t.Errorf("expected Locked 0, got %d", szOut.Locked)
	}
}

func TestLifecycle_DisconnectPacket(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	dbQueue := make(chan *database.DBWriteJob, 10)
	s.dbQueue = dbQueue
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30031}
	sessID := uint32(601)
	sess := &network.PlayerSession{
		SessionID:    sessID,
		AccountID:    "uuid-dc-player",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{15.0, 1.0, 25.0},
		Rotation:     [2]float32{1.5, 0.0},
		Health:       85.0,
		InSafeZone:   true,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)
	s.grid.Insert(sessID, 15.0, 25.0)

	// Add another peer session on the same level to receive leave broadcast
	peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30032}
	peerID := uint32(602)
	peerSess := &network.PlayerSession{
		SessionID:    peerID,
		AccountID:    "uuid-peer-player",
		UDPAddr:      peerAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{16.0, 1.0, 26.0},
		Health:       100.0,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(peerSess)
	s.grid.Insert(peerID, 16.0, 26.0)

	if !s.grid.Contains(sessID) {
		t.Fatalf("expected session to be in spatial grid prior to disconnect")
	}

	sink.Reset()

	// Send OpDisconnect
	rawDC := buildTestPacket(t, protocol.OpDisconnect, 1, protocol.FlagReliable, uint8(0))
	s.HandlePacket(rawDC, addr)

	// 1. Verify session removed from sessions map
	if s.sessions.GetByID(sessID) != nil {
		t.Errorf("expected session %d to be removed from sessions map", sessID)
	}
	if s.sessions.GetByAddr(addr.String()) != nil {
		t.Errorf("expected addr %s to be removed from sessions byAddr map", addr.String())
	}

	// 2. Verify session removed from spatial grid
	if s.grid.Contains(sessID) {
		t.Errorf("expected session %d to be removed from spatial grid", sessID)
	}

	// 3. Verify transform queued
	if len(dbQueue) == 0 {
		t.Errorf("expected transform to be queued in DB write queue upon disconnect")
	} else {
		job := <-dbQueue
		if !strings.Contains(job.Query, "UPDATE characters") {
			t.Errorf("expected UPDATE characters query, got '%s'", job.Query)
		}
	}

	// 4. Verify OpLeave (OpEntityLeaveAoI, 0x0013) was broadcast to peer
	if sink.PacketCount() == 0 {
		t.Fatalf("expected OpLeave broadcast to peer, got no packets")
	}
	rLeave := bytes.NewReader(sink.LastPacket())
	hdrLeave, err := protocol.ReadHeader(rLeave)
	if err != nil {
		t.Fatalf("failed to read leave packet header: %v", err)
	}
	if hdrLeave.Opcode != protocol.OpEntityLeaveAoI {
		t.Errorf("expected Opcode OpEntityLeaveAoI (0x0013), got 0x%04X", hdrLeave.Opcode)
	}
	var leavePkt protocol.EntityLeaveAoI
	if err := binary.Read(rLeave, binary.LittleEndian, &leavePkt); err != nil {
		t.Fatalf("failed to read EntityLeaveAoI payload: %v", err)
	}
	if leavePkt.EntityID != sessID {
		t.Errorf("expected leave EntityID %d, got %d", sessID, leavePkt.EntityID)
	}
}

func TestLifecycle_StaleSessionTimeout(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30041}
	sessID := uint32(701)
	sess := &network.PlayerSession{
		SessionID:    sessID,
		AccountID:    "uuid-stale-lifecycle",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		// Inside sz_cordon_rookie so timeout does NOT spawn a sleeper
		// (sleepers re-insert the entity ID into the grid by design).
		Position:     [3]float32{-211.3, -20.2, -145.8},
		Health:       100.0,
		LastSeen:     time.Now().Add(-35 * time.Second),
	}
	s.sessions.AddSession(sess)
	s.grid.Insert(sessID, -211.3, -145.8)

	if !s.grid.Contains(sessID) {
		t.Fatalf("expected session to be in grid before tick")
	}

	// Run Tick to trigger stale session cleanup
	s.Tick(time.Now())

	// Verify removed from sessions
	if s.sessions.GetByID(sessID) != nil {
		t.Errorf("expected stale session to be removed from sessions on timeout")
	}

	// Verify removed from grid
	if s.grid.Contains(sessID) {
		t.Errorf("expected stale session to be removed from spatial grid on timeout")
	}
}

func TestLifecycle_BannedPlayerRejected(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	sink.Reset()

	bannedUUID := "uuid-banned-stalker-99"
	_ = db.AutoProvision(bannedUUID, "hwid-banned", "BadActor")
	if err := db.BanAccount(bannedUUID, "Aimbot detected"); err != nil {
		t.Fatalf("Failed to ban account: %v", err)
	}

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30051}
	var req protocol.HandshakeReq
	copy(req.UUID[:], bannedUUID)
	copy(req.Nickname[:], "BadActor")
	req.ProtocolVer = 1

	raw := buildTestPacket(t, protocol.OpHandshakeReq, 1, protocol.FlagReliable, req)
	s.HandlePacket(raw, addr)

	// Session should NOT be added
	if s.sessions.GetByAddr(addr.String()) != nil {
		t.Errorf("Expected banned player to not have active session")
	}

	// Verify response was Status 2 (Banned)
	if sink.PacketCount() == 0 {
		t.Fatalf("Expected handshake response packet")
	}
	r := bytes.NewReader(sink.LastPacket())
	_, _ = protocol.ReadHeader(r)
	var res protocol.HandshakeRes
	_ = binary.Read(r, binary.LittleEndian, &res)
	if res.Status != 2 {
		t.Errorf("Expected HandshakeRes Status 2 (Banned), got %d", res.Status)
	}
}

func TestLifecycle_CombatDisconnectSleeper(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30061}
	sessID := uint32(801)
	uuid := "uuid-sleeper-combat-dc"
	sess := &network.PlayerSession{
		SessionID:    sessID,
		AccountID:    uuid,
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{100.0, 5.0, 100.0},
		Rotation:     [2]float32{0.0, 0.0},
		Health:       90.0,
		InSafeZone:   false, // Disconnecting outside safe zone
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)
	s.grid.Insert(sessID, 100.0, 100.0)

	// Disconnect outside safe zone
	rawDC := buildTestPacket(t, protocol.OpDisconnect, 1, protocol.FlagReliable, uint8(0))
	s.HandlePacket(rawDC, addr)

	// 1. Verify player session removed
	if s.sessions.GetByID(sessID) != nil {
		t.Errorf("Expected session %d removed from active sessions", sessID)
	}

	// 2. Verify sleeper proxy was spawned in SleeperManager
	sleeper := s.Sleepers().GetSleeperByAccount(uuid)
	if sleeper == nil {
		t.Fatalf("Expected sleeper proxy to be created for account %s", uuid)
	}
	if sleeper.Position != [3]float32{100.0, 5.0, 100.0} {
		t.Errorf("Expected sleeper position [100, 5, 100], got %v", sleeper.Position)
	}
	if sleeper.Health != 90.0 {
		t.Errorf("Expected sleeper health 90, got %f", sleeper.Health)
	}

	// 3. Sleeper takes damage in world
	s.Sleepers().ApplyDamage(sleeper.EntityID, 20.0)
	if sleeper.Health != 70.0 {
		t.Errorf("Expected sleeper health 70 after 20 damage, got %f", sleeper.Health)
	}

	// 4. Reconnect with same account: sleeper should be reclaimed
	newAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30062}
	var req protocol.HandshakeReq
	copy(req.UUID[:], uuid)
	copy(req.Nickname[:], "ReturningStalker")
	req.ProtocolVer = 1

	rawHS := buildTestPacket(t, protocol.OpHandshakeReq, 2, protocol.FlagReliable, req)
	s.HandlePacket(rawHS, newAddr)

	reconnectedSess := s.sessions.GetByAddr(newAddr.String())
	if reconnectedSess == nil {
		t.Fatalf("Expected reconnected session to exist")
	}
	if reconnectedSess.Health != 70.0 {
		t.Errorf("Expected reconnected player to have sleeper health 70.0, got %f", reconnectedSess.Health)
	}
	// Sleeper must now be removed from SleeperManager
	if s.Sleepers().GetSleeperByAccount(uuid) != nil {
		t.Errorf("Expected sleeper to be removed upon player reconnection")
	}
}

func TestLifecycle_SpeedhackRubberband(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30071}
	sessID := uint32(901)
	sess := &network.PlayerSession{
		SessionID:         sessID,
		AccountID:         "uuid-speedhack-test",
		UDPAddr:           addr,
		CurrentLevel:      "l01_escape",
		Position:          [3]float32{0.0, 0.0, 0.0},
		LastSeen:          time.Now().Add(-1 * time.Second),
		LastTransformTime: time.Now().Add(-1 * time.Second),
	}
	s.sessions.AddSession(sess)
	s.grid.Insert(sessID, 0.0, 0.0)

	// Valid initial move
	ct1 := protocol.ClientTransform{
		PosX: 5.0,
		PosY: 0.0,
		PosZ: 5.0,
	}
	binary.LittleEndian.PutUint32(ct1.SessionID[:], sessID)
	raw1 := buildTestPacket(t, protocol.OpClientTransform, 1, protocol.FlagUnreliable, ct1)
	s.HandlePacket(raw1, addr)

	sess.Lock()
	p1 := sess.Position
	sess.Unlock()
	if p1[0] != 5.0 || p1[2] != 5.0 {
		t.Fatalf("Expected valid transform position (5, 5), got (%f, %f)", p1[0], p1[2])
	}

	// Speedhack move: jump 500m in 0.01s (50,000 m/s > 25 m/s)
	sess.Lock()
	sess.LastTransformTime = time.Now().Add(-10 * time.Millisecond)
	sess.Unlock()

	ctHack := protocol.ClientTransform{
		PosX: 500.0,
		PosY: 0.0,
		PosZ: 500.0,
	}
	binary.LittleEndian.PutUint32(ctHack.SessionID[:], sessID)
	rawHack := buildTestPacket(t, protocol.OpClientTransform, 2, protocol.FlagUnreliable, ctHack)
	s.HandlePacket(rawHack, addr)

	// Position must be rubberbanded (kept at 5, 5, NOT 500, 500)
	sess.Lock()
	pHack := sess.Position
	sess.Unlock()
	if pHack[0] != 5.0 || pHack[2] != 5.0 {
		t.Errorf("Speedhack packet was not rubberbanded! Position: (%f, %f)", pHack[0], pHack[2])
	}

	// Anticheat violation should be recorded
	if s.anticheat.ViolationCount(sessID) == 0 {
		t.Errorf("Expected violation recorded for speedhack")
	}
}

func TestLifecycle_DamageSafeZoneImmunity(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	attackerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30081}
	targetAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 30082}

	attackerSess := &network.PlayerSession{
		SessionID:    1001,
		AccountID:    "attacker-uuid",
		UDPAddr:      attackerAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{10.0, 0.0, 10.0},
		Health:       100.0,
		InSafeZone:   false,
	}
	targetSess := &network.PlayerSession{
		SessionID:    1002,
		AccountID:    "victim-uuid",
		UDPAddr:      targetAddr,
		CurrentLevel: "l01_escape",
		Position:     [3]float32{15.0, 0.0, 15.0},
		Health:       100.0,
		InSafeZone:   true, // Victim inside safe zone
	}

	s.sessions.AddSession(attackerSess)
	s.sessions.AddSession(targetSess)

	dmg := protocol.DamageNotify{
		TargetID:   1002,
		AttackerID: 1001,
		Damage:     50.0,
		BoneID:     1,
	}
	rawDmg := buildTestPacket(t, protocol.OpDamageNotify, 1, protocol.FlagReliable, dmg)
	s.HandlePacket(rawDmg, attackerAddr)

	targetSess.Lock()
	h := targetSess.Health
	targetSess.Unlock()

	// Safe zone immunity: health must remain 100
	if h != 100.0 {
		t.Errorf("Expected safe zone victim health 100.0, got %f", h)
	}

	// Now move target outside safe zone
	targetSess.Lock()
	targetSess.InSafeZone = false
	targetSess.Unlock()

	sink.Reset()
	rawDmg2 := buildTestPacket(t, protocol.OpDamageNotify, 2, protocol.FlagReliable, dmg)
	s.HandlePacket(rawDmg2, attackerAddr)

	targetSess.Lock()
	h2 := targetSess.Health
	targetSess.Unlock()

	if h2 != 50.0 {
		t.Errorf("Expected target health 50.0 after valid damage, got %f", h2)
	}
}
