package game

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

func doJoinHandshake(t *testing.T, s *Server, addr *net.UDPAddr, uuid, nick string) {
	t.Helper()
	var req protocol.HandshakeReq
	copy(req.UUID[:], uuid)
	copy(req.Nickname[:], nick)
	req.ProtocolVer = protocol.ProtocolVer
	s.HandlePacket(buildTestPacket(t, protocol.OpHandshakeReq, 1, protocol.FlagReliable, req), addr)
}

func packetsByOpcode(sink *fakeSink, op uint16) [][]byte {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out [][]byte
	for _, raw := range sink.sent {
		r := bytes.NewReader(raw)
		h, err := protocol.ReadHeader(r)
		if err != nil || h.Opcode != op {
			continue
		}
		out = append(out, raw)
	}
	return out
}

func packetPayload(t *testing.T, raw []byte) []byte {
	t.Helper()
	if len(raw) < 12 {
		t.Fatalf("packet too short: %d bytes", len(raw))
	}
	return raw[12:]
}

func readLevelLoad(t *testing.T, raw []byte) protocol.LevelLoad {
	t.Helper()
	var p protocol.LevelLoad
	if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &p); err != nil {
		t.Fatalf("read LevelLoad: %v", err)
	}
	return p
}

func readInventorySyncItems(t *testing.T, raw []byte) []protocol.InventoryItemPayload {
	t.Helper()
	p := packetPayload(t, raw)
	if len(p) < 1 {
		t.Fatalf("InventorySync payload empty")
	}
	count := int(p[0])
	pos := 1
	entrySize := 64 + 2 + 1 + 2 + 1
	if len(p) < pos+count*entrySize {
		t.Fatalf("InventorySync payload truncated: count=%d len=%d", count, len(p))
	}
	items := make([]protocol.InventoryItemPayload, 0, count)
	for i := 0; i < count; i++ {
		var it protocol.InventoryItemPayload
		if err := binary.Read(bytes.NewReader(p[pos:pos+entrySize]), binary.LittleEndian, &it); err != nil {
			t.Fatalf("read inventory entry %d: %v", i, err)
		}
		items = append(items, it)
		pos += entrySize
	}
	return items
}

func countSyncItems(t *testing.T, syncs [][]byte) (int, map[string]int) {
	t.Helper()
	total := 0
	sections := map[string]int{}
	for _, raw := range syncs {
		for _, it := range readInventorySyncItems(t, raw) {
			total++
			sec := string(bytes.TrimRight(it.Section[:], "\x00"))
			sections[sec]++
		}
	}
	return total, sections
}

func TestJoinFlow_NewPlayerShowStartThenLoadLevel(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31001}
	doJoinHandshake(t, s, addr, "uuid-join-new", "Newbie")

	if got := len(packetsByOpcode(sink, protocol.OpShowStart)); got != 1 {
		t.Fatalf("expected 1 ShowStart for new player, got %d", got)
	}
	if got := len(packetsByOpcode(sink, protocol.OpLoadLevel)); got != 0 {
		t.Fatalf("expected no LoadLevel before CharacterSelect, got %d", got)
	}
	if got := len(packetsByOpcode(sink, protocol.OpInventorySync)); got != 0 {
		t.Fatalf("expected no InventorySync before CharacterSelect, got %d", got)
	}

	var sel protocol.CharacterSelect
	copy(sel.Faction[:], "dolg")
	sel.Money = 3456
	copy(sel.Items[:], "medkit:2,bandage:3")
	s.HandlePacket(buildTestPacket(t, protocol.OpCharacterSelect, 2, protocol.FlagReliable, sel), addr)

	loads := packetsByOpcode(sink, protocol.OpLoadLevel)
	if len(loads) != 1 {
		t.Fatalf("expected 1 LoadLevel after CharacterSelect, got %d", len(loads))
	}
	ll := readLevelLoad(t, loads[0])
	if ll.Level != 0 {
		t.Errorf("expected level id 0 (l01_escape), got %d", ll.Level)
	}
	if faction := string(bytes.TrimRight(ll.Faction[:], "\x00")); faction != "dolg" {
		t.Errorf("expected faction dolg in LoadLevel, got %q", faction)
	}
	if ll.PosX != -211.3 || ll.PosY != -20.2 || ll.PosZ != -145.8 {
		t.Errorf("unexpected spawn pos in LoadLevel: %v %v %v", ll.PosX, ll.PosY, ll.PosZ)
	}
	if ll.EcoTier != 1 {
		t.Errorf("expected eco tier 1, got %d", ll.EcoTier)
	}

	total, sections := countSyncItems(t, packetsByOpcode(sink, protocol.OpInventorySync))
	if total != 2 {
		t.Fatalf("expected 2 starter inventory entries, got %d", total)
	}
	if sections["medkit"] != 1 || sections["bandage"] != 1 {
		t.Errorf("unexpected starter sections: %v", sections)
	}

	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatal("session missing")
	}
	sess.Lock()
	pending := sess.PendingCreate
	sess.Unlock()
	if pending {
		t.Error("expected PendingCreate cleared after successful CharacterSelect")
	}
	if !s.grid.Contains(sess.SessionID) {
		t.Error("expected session inserted into spatial grid after spawn flow")
	}

	char, err := db.LoadCharacter("uuid-join-new")
	if err != nil {
		t.Fatalf("LoadCharacter: %v", err)
	}
	if char.Faction != "dolg" {
		t.Errorf("expected persisted faction dolg, got %q", char.Faction)
	}
	var rubles int
	if err := db.RawDB().QueryRow("SELECT rubles FROM characters WHERE client_uuid=?", "uuid-join-new").Scan(&rubles); err != nil {
		t.Fatalf("read rubles: %v", err)
	}
	if rubles != 3456 {
		t.Errorf("expected starter money 3456, got %d", rubles)
	}
	items, err := db.GetCharacterInventory("uuid-join-new")
	if err != nil {
		t.Fatalf("GetCharacterInventory: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 persisted inventory rows, got %d", len(items))
	}
}

func TestJoinFlow_ReturningPlayerGetsLoadLevelAndInventory(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-join-return", "hwid", "Veteran"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-join-return", "freedom", "wpn_ak74:30,ammo_5.45x39_fmj:60", 7777); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31002}
	doJoinHandshake(t, s, addr, "uuid-join-return", "Veteran")

	if got := len(packetsByOpcode(sink, protocol.OpShowStart)); got != 0 {
		t.Fatalf("expected no ShowStart for returning player, got %d", got)
	}
	loads := packetsByOpcode(sink, protocol.OpLoadLevel)
	if len(loads) != 1 {
		t.Fatalf("expected 1 LoadLevel immediately after handshake, got %d", len(loads))
	}
	ll := readLevelLoad(t, loads[0])
	if faction := string(bytes.TrimRight(ll.Faction[:], "\x00")); faction != "freedom" {
		t.Errorf("expected faction freedom, got %q", faction)
	}
	total, sections := countSyncItems(t, packetsByOpcode(sink, protocol.OpInventorySync))
	if total != 2 {
		t.Fatalf("expected 2 inventory entries, got %d", total)
	}
	if sections["wpn_ak74"] != 1 || sections["ammo_5.45x39_fmj"] != 1 {
		t.Errorf("unexpected sections: %v", sections)
	}
}

func TestJoinFlow_InvalidFactionRejectedExplicitly(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31003}
	doJoinHandshake(t, s, addr, "uuid-join-badfaction", "BadActor")

	var sel protocol.CharacterSelect
	copy(sel.Faction[:], "nazi")
	s.HandlePacket(buildTestPacket(t, protocol.OpCharacterSelect, 2, protocol.FlagReliable, sel), addr)

	errs := packetsByOpcode(sink, protocol.OpError)
	if len(errs) != 1 {
		t.Fatalf("expected 1 explicit OpError, got %d", len(errs))
	}
	if code := packetPayload(t, errs[0])[0]; code != protocolErrorInvalidFaction {
		t.Errorf("expected error code %d, got %d", protocolErrorInvalidFaction, code)
	}
	if got := len(packetsByOpcode(sink, protocol.OpLoadLevel)); got != 0 {
		t.Errorf("expected no LoadLevel after rejected select, got %d", got)
	}
	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatal("session missing")
	}
	sess.Lock()
	pending := sess.PendingCreate
	sess.Unlock()
	if !pending {
		t.Error("expected PendingCreate kept true so the player can retry")
	}
}

func TestJoinFlow_InvalidLoadoutRejectedExplicitly(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31004}
	doJoinHandshake(t, s, addr, "uuid-join-badloadout", "BadKit")

	var sel protocol.CharacterSelect
	copy(sel.Faction[:], "stalker")
	copy(sel.Items[:], "bad section!")
	s.HandlePacket(buildTestPacket(t, protocol.OpCharacterSelect, 2, protocol.FlagReliable, sel), addr)

	errs := packetsByOpcode(sink, protocol.OpError)
	if len(errs) != 1 {
		t.Fatalf("expected 1 explicit OpError, got %d", len(errs))
	}
	if code := packetPayload(t, errs[0])[0]; code != protocolErrorInvalidLoadout {
		t.Errorf("expected error code %d, got %d", protocolErrorInvalidLoadout, code)
	}
	if msg := string(bytes.TrimRight(packetPayload(t, errs[0])[1:], "\x00")); !strings.Contains(msg, "rejected") {
		t.Errorf("expected human-readable error message, got %q", msg)
	}
}

func TestJoinFlow_LevelReportDoesNotResendInventory(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-join-reload", "hwid", "Reloader"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-join-reload", "stalker", "medkit:1", 0); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31006}
	doJoinHandshake(t, s, addr, "uuid-join-reload", "Reloader")
	if initial := len(packetsByOpcode(sink, protocol.OpInventorySync)); initial != 1 {
		t.Fatalf("expected exactly 1 InventorySync after handshake, got %d", initial)
	}

	// The client reports its level after the level is already synced; a
	// same-level report must not redeliver the kit (no double delivery).
	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], "l01_escape")
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 3, protocol.FlagReliable, lvl), addr)
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 4, protocol.FlagReliable, lvl), addr)
	if got := len(packetsByOpcode(sink, protocol.OpInventorySync)); got != 1 {
		t.Fatalf("expected same-level reports to keep exactly 1 delivery, got %d", got)
	}

	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatal("session missing")
	}
	sess.Lock()
	synced := sess.InventorySyncedLevel
	sess.Unlock()
	if synced != "l01_escape" {
		t.Errorf("expected synced level l01_escape, got %q", synced)
	}
}

func TestJoinFlow_InitialSendOnceAndNewLevelResends(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-join-newlevel", "hwid", "Traveller"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-join-newlevel", "stalker", "medkit:1", 0); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31007}
	doJoinHandshake(t, s, addr, "uuid-join-newlevel", "Traveller")
	if initial := len(packetsByOpcode(sink, protocol.OpInventorySync)); initial != 1 {
		t.Fatalf("expected initial send exactly once, got %d", initial)
	}

	// A report for a genuinely different supported level re-sends the kit.
	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], "l02_garbage")
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 3, protocol.FlagReliable, lvl), addr)
	if got := len(packetsByOpcode(sink, protocol.OpInventorySync)); got != 2 {
		t.Fatalf("expected inventory resend on new level, got %d deliveries", got)
	}

	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatal("session missing")
	}
	sess.Lock()
	level := sess.CurrentLevel
	synced := sess.InventorySyncedLevel
	sess.Unlock()
	if level != "l02_garbage" {
		t.Errorf("expected level l02_garbage, got %q", level)
	}
	if synced != "l02_garbage" {
		t.Errorf("expected synced level l02_garbage, got %q", synced)
	}
}

func TestJoinFlow_LevelChangeRejectedDuringPendingCreate(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31008}
	doJoinHandshake(t, s, addr, "uuid-join-pending", "Newbie")

	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], "l01_escape")
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 3, protocol.FlagReliable, lvl), addr)

	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatal("session missing")
	}
	sess.Lock()
	pending := sess.PendingCreate
	sess.Unlock()
	if !pending {
		t.Fatal("expected PendingCreate to survive the rejected level change")
	}
	if got := len(packetsByOpcode(sink, protocol.OpInventorySync)); got != 0 {
		t.Errorf("expected no InventorySync while character creation is pending, got %d", got)
	}
}

func TestJoinFlow_UnknownLevelIgnored(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-join-unknownlv", "hwid", "Wanderer"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-join-unknownlv", "stalker", "medkit:1", 0); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31009}
	doJoinHandshake(t, s, addr, "uuid-join-unknownlv", "Wanderer")
	if initial := len(packetsByOpcode(sink, protocol.OpInventorySync)); initial != 1 {
		t.Fatalf("expected 1 InventorySync after handshake, got %d", initial)
	}

	var lvl protocol.LevelChangePayload
	copy(lvl.Level[:], "not_a_real_level")
	s.HandlePacket(buildTestPacket(t, protocol.OpLevelChange, 3, protocol.FlagReliable, lvl), addr)

	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatal("session missing")
	}
	sess.Lock()
	level := sess.CurrentLevel
	sess.Unlock()
	if level != "l01_escape" {
		t.Errorf("unknown level must be ignored, current level %q", level)
	}
	if got := len(packetsByOpcode(sink, protocol.OpInventorySync)); got != 1 {
		t.Errorf("expected no resync for unknown level, got %d deliveries", got)
	}
}

func TestJoinFlow_InventorySyncChunksUnderMTU(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	loadout := strings.TrimSuffix(strings.Repeat("medkit,", 20), ",")
	if err := db.AutoProvision("uuid-join-chunks", "hwid", "Packer"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-join-chunks", "stalker", loadout, 0); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31005}
	doJoinHandshake(t, s, addr, "uuid-join-chunks", "Packer")

	syncs := packetsByOpcode(sink, protocol.OpInventorySync)
	if len(syncs) != 2 {
		t.Fatalf("expected 20 items split into 2 packets, got %d packets", len(syncs))
	}
	if got := len(readInventorySyncItems(t, syncs[0])); got != protocol.MaxInventorySyncItems {
		t.Errorf("expected first chunk %d items, got %d", protocol.MaxInventorySyncItems, got)
	}
	if got := len(readInventorySyncItems(t, syncs[1])); got != 20-protocol.MaxInventorySyncItems {
		t.Errorf("expected second chunk %d items, got %d", 20-protocol.MaxInventorySyncItems, got)
	}
	for _, raw := range syncs {
		if len(raw) > protocol.MaxSafeUDPPacketSize {
			t.Errorf("inventory sync packet exceeds MTU: %d bytes", len(raw))
		}
	}
	total, _ := countSyncItems(t, syncs)
	if total != 20 {
		t.Errorf("expected 20 total items, got %d", total)
	}
}

// A peer that has not yet sent a transform/visual (gvid 0 or no visual) must
// not be announced to a newcomer; once it has both, it is.
func TestJoinFlow_NewcomerSkipsPeersWithoutVisual(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	if err := db.AutoProvision("uuid-newcomer-vis", "hwid", "Newcomer"); err != nil {
		t.Fatalf("AutoProvision: %v", err)
	}
	if err := db.CreateCharacter("uuid-newcomer-vis", "stalker", "medkit:1", 0); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}

	peerID := uint32(8801)
	peer := &network.PlayerSession{
		SessionID:    peerID,
		AccountID:    "uuid-newcomer-peer",
		Name:         "Peer",
		UDPAddr:      &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31011},
		CurrentLevel: "l01_escape",
		Position:     [3]float32{0, 0, 0},
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(peer)
	sink.Reset()

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31010}
	doJoinHandshake(t, s, addr, "uuid-newcomer-vis", "Newcomer")

	for _, raw := range packetsByOpcode(sink, protocol.OpEntityEnterAoI) {
		var enter protocol.EntityEnterAoI
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &enter); err != nil {
			t.Fatalf("read EntityEnterAoI: %v", err)
		}
		if enter.EntityID == peerID {
			t.Fatal("peer without gvid/visual was announced to newcomer")
		}
	}

	// After the peer sends a transform with gvid and a visual, the newcomer
	// sees it when peers are replayed.
	peer.Lock()
	peer.Gvid = 0x55
	peer.HasVisual = true
	copy(peer.Visual[:], `actors\stalker_neutral\stalker_neutral_1`)
	peer.Unlock()
	sink.Reset()
	s.sendExistingPeersToNewcomer(s.sessions.GetByAddr(addr.String()))

	found := false
	for _, raw := range packetsByOpcode(sink, protocol.OpEntityEnterAoI) {
		var enter protocol.EntityEnterAoI
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &enter); err != nil {
			t.Fatalf("read EntityEnterAoI: %v", err)
		}
		if enter.EntityID == peerID && enter.Gvid == 0x55 {
			found = true
		}
	}
	if !found {
		t.Fatal("newcomer did not receive ENTITY_ENTER for peer with gvid+visual")
	}
}
