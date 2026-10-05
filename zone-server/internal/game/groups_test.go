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

type testMember struct {
	addr *net.UDPAddr
	sess *network.PlayerSession
	name string
}

func connectMember(t *testing.T, s *Server, port int, uuid, nick string) testMember {
	t.Helper()
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	doJoinHandshake(t, s, addr, uuid, nick)
	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil {
		t.Fatalf("session for %s not registered", nick)
	}
	return testMember{addr: addr, sess: sess, name: nick}
}

func sendChatCommand(t *testing.T, s *Server, sess *network.PlayerSession, seq uint32, text string) {
	t.Helper()
	pkt := protocol.NewChatText(sessionIDOf(sess), text)
	s.HandlePacket(buildTestPacket(t, protocol.OpChatText, seq, protocol.FlagReliable, pkt), sess.UDPAddr)
}

func parseGroupState(t *testing.T, raw []byte) protocol.GroupState {
	t.Helper()
	r := bytes.NewReader(raw)
	if _, err := protocol.ReadHeader(r); err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	state, err := protocol.ReadGroupState(r)
	if err != nil {
		t.Fatalf("ReadGroupState: %v", err)
	}
	return *state
}

func parseChatText(t *testing.T, raw []byte) protocol.ChatText {
	t.Helper()
	r := bytes.NewReader(raw)
	if _, err := protocol.ReadHeader(r); err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	var pkt protocol.ChatText
	if err := binary.Read(r, binary.LittleEndian, &pkt); err != nil {
		t.Fatalf("ChatText read: %v", err)
	}
	return pkt
}

func trimField(b []byte) string {
	return string(bytes.TrimRight(b, "\x00"))
}

func TestGroupInviteAcceptLeaveFlow(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	alpha := connectMember(t, s, 46001, "uuid-group-flow-a", "Alpha")
	bravo := connectMember(t, s, 46002, "uuid-group-flow-b", "Bravo")

	alpha.sess.Lock()
	alpha.sess.Faction = "dolg"
	alpha.sess.Unlock()
	bravo.sess.Lock()
	bravo.sess.Faction = "bandit"
	bravo.sess.Unlock()
	sink.Reset()

	// /invite delivers OpGroupInviteNotify to the target with identity bytes.
	sendChatCommand(t, s, alpha.sess, 2, "/invite Bravo")
	raw := findOpcodePacket(t, sink, protocol.OpGroupInviteNotify)
	if raw == nil {
		t.Fatal("target did not receive OpGroupInviteNotify")
	}
	r := bytes.NewReader(raw)
	if _, err := protocol.ReadHeader(r); err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	var inv protocol.GroupInviteNotify
	if err := binary.Read(r, binary.LittleEndian, &inv); err != nil {
		t.Fatalf("GroupInviteNotify read: %v", err)
	}
	if inv.InviterSessionID != sessionIDOf(alpha.sess) {
		t.Errorf("inviter session = %d, want %d", inv.InviterSessionID, sessionIDOf(alpha.sess))
	}
	if got := trimField(inv.InviterName[:]); got != "Alpha" {
		t.Errorf("inviter name = %q, want Alpha", got)
	}
	if got := trimField(inv.InviterFaction[:]); got != "dolg" {
		t.Errorf("inviter faction = %q, want dolg", got)
	}

	// /accept creates the group and pushes OpGroupState to both members.
	sendChatCommand(t, s, bravo.sess, 2, "/accept")
	states := packetsByOpcode(sink, protocol.OpGroupState)
	if len(states) != 2 {
		t.Fatalf("expected both members to receive OpGroupState, got %d", len(states))
	}
	for _, raw := range states {
		state := parseGroupState(t, raw)
		if state.MemberCount != 2 {
			t.Fatalf("group state memberCount = %d, want 2", state.MemberCount)
		}
		if state.Members[0].SessionID != sessionIDOf(alpha.sess) || state.Members[0].IsLeader != 1 {
			t.Errorf("first member = %+v, want Alpha as leader", state.Members[0])
		}
		if state.Members[1].SessionID != sessionIDOf(bravo.sess) || state.Members[1].IsLeader != 0 {
			t.Errorf("second member = %+v, want Bravo non-leader", state.Members[1])
		}
		if got := trimField(state.Members[0].Name[:]); got != "Alpha" {
			t.Errorf("member 0 name = %q", got)
		}
		if got := trimField(state.Members[1].Faction[:]); got != "bandit" {
			t.Errorf("member 1 faction = %q", got)
		}
	}

	gid, ok := s.groups.GroupID(sessionIDOf(alpha.sess))
	if !ok {
		t.Fatal("Alpha has no group after accept")
	}
	if got := len(s.groups.Members(gid)); got != 2 {
		t.Fatalf("group size = %d, want 2", got)
	}

	// /leave by the leader dissolves the group and sends the empty state to
	// every affected player.
	sink.Reset()
	sendChatCommand(t, s, alpha.sess, 3, "/leave")
	states = packetsByOpcode(sink, protocol.OpGroupState)
	if len(states) == 0 {
		t.Fatal("no OpGroupState after /leave")
	}
	for _, raw := range states {
		state := parseGroupState(t, raw)
		if state.MemberCount != 0 {
			t.Fatalf("post-leave state memberCount = %d, want 0", state.MemberCount)
		}
	}
	if _, ok := s.groups.GroupID(sessionIDOf(alpha.sess)); ok {
		t.Fatal("group did not dissolve after leader left")
	}
	if _, ok := s.groups.GroupID(sessionIDOf(bravo.sess)); ok {
		t.Fatal("Bravo still grouped after leader left")
	}
}

func TestGroupMaxEnforced(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	leader := connectMember(t, s, 46100, "uuid-max-leader", "Leader")
	bravo := connectMember(t, s, 46101, "uuid-max-bravo", "Bravo")
	charlie := connectMember(t, s, 46102, "uuid-max-charlie", "Charlie")
	delta := connectMember(t, s, 46103, "uuid-max-delta", "Delta")
	echo := connectMember(t, s, 46104, "uuid-max-echo", "Echo")

	// Fill the group to the default 4-player cap.
	sendChatCommand(t, s, leader.sess, 2, "/invite Bravo")
	sendChatCommand(t, s, bravo.sess, 2, "/accept")
	sendChatCommand(t, s, leader.sess, 3, "/invite Charlie")
	sendChatCommand(t, s, charlie.sess, 2, "/accept")

	// Invite the 4th and 5th players while at 3 members so the pending
	// acceptance is what hits the cap, then reject it with a system reply.
	sendChatCommand(t, s, leader.sess, 4, "/invite Delta")
	sendChatCommand(t, s, leader.sess, 5, "/invite Echo")
	sendChatCommand(t, s, delta.sess, 2, "/accept")

	gid, ok := s.groups.GroupID(sessionIDOf(leader.sess))
	if !ok {
		t.Fatal("leader has no group")
	}
	if got := len(s.groups.Members(gid)); got != 4 {
		t.Fatalf("group size after 4 members = %d, want 4", got)
	}

	sink.Reset()
	sendChatCommand(t, s, echo.sess, 2, "/accept")
	chats := packetsByOpcode(sink, protocol.OpChatText)
	if len(chats) != 1 {
		t.Fatalf("expected one system reply to the 5th player, got %d chat packets", len(chats))
	}
	pkt := parseChatText(t, chats[0])
	if pkt.SenderID != 0 {
		t.Errorf("system reply sender = %d, want 0", pkt.SenderID)
	}
	if text := string(pkt.Text[:pkt.Len]); !strings.Contains(strings.ToLower(text), "full") {
		t.Errorf("system reply = %q, want a group-full notice", text)
	}
	if got := len(s.groups.Members(gid)); got != 4 {
		t.Fatalf("group grew past cap: size = %d, want 4", got)
	}
}

func TestChatCommand_InviteUnknownNickRepliesToSenderOnly(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	alpha := connectMember(t, s, 46201, "uuid-cmd-unknown-a", "Alpha")
	bravo := connectMember(t, s, 46202, "uuid-cmd-unknown-b", "Bravo")
	_ = bravo
	sink.Reset()

	sendChatCommand(t, s, alpha.sess, 2, "/invite NobodyHere")

	chats := packetsByOpcode(sink, protocol.OpChatText)
	if len(chats) != 1 {
		t.Fatalf("expected reply to sender only, got %d chat packets", len(chats))
	}
	pkt := parseChatText(t, chats[0])
	if pkt.SenderID != 0 {
		t.Errorf("system reply sender = %d, want 0", pkt.SenderID)
	}
	if text := string(pkt.Text[:pkt.Len]); !strings.Contains(text, "NobodyHere") {
		t.Errorf("reply = %q, want unknown-nick notice", text)
	}
	if len(packetsByOpcode(sink, protocol.OpGroupInviteNotify)) != 0 {
		t.Fatal("invite notify sent for unknown nickname")
	}
}

func TestChatCommand_UnknownSlashCommandRepliesToSenderOnly(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	alpha := connectMember(t, s, 46203, "uuid-cmd-unknown-c", "Alpha")
	_ = connectMember(t, s, 46204, "uuid-cmd-unknown-d", "Bravo")
	sink.Reset()

	sendChatCommand(t, s, alpha.sess, 2, "/wiggle")

	chats := packetsByOpcode(sink, protocol.OpChatText)
	if len(chats) != 1 {
		t.Fatalf("expected reply to sender only, got %d chat packets", len(chats))
	}
	pkt := parseChatText(t, chats[0])
	if pkt.SenderID != 0 {
		t.Errorf("system reply sender = %d, want 0", pkt.SenderID)
	}
	if text := string(pkt.Text[:pkt.Len]); !strings.Contains(text, "/wiggle") {
		t.Errorf("reply = %q, want unknown-command notice", text)
	}
}

func TestGroupOpResponseAccepts(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	alpha := connectMember(t, s, 46300, "uuid-op-resp-a", "Alpha")
	bravo := connectMember(t, s, 46301, "uuid-op-resp-b", "Bravo")

	sendChatCommand(t, s, alpha.sess, 2, "/invite Bravo")
	sink.Reset()

	resp := protocol.GroupResponse{Accept: 1}
	s.HandlePacket(buildTestPacket(t, protocol.OpGroupResponse, 2, protocol.FlagReliable, resp), bravo.addr)

	if gid, ok := s.groups.GroupID(sessionIDOf(bravo.sess)); !ok || len(s.groups.Members(gid)) != 2 {
		t.Fatal("OpGroupResponse(accept=1) did not join the group")
	}
	if len(packetsByOpcode(sink, protocol.OpGroupState)) == 0 {
		t.Fatal("no OpGroupState after OpGroupResponse accept")
	}
}

func TestGroupInviteExpiresOnTick(t *testing.T) {
	gm := NewGroupManager(4, 10*time.Millisecond)
	inviter := &network.PlayerSession{SessionID: 9301, Name: "Inviter"}
	target := &network.PlayerSession{SessionID: 9302, Name: "Target"}
	if err := gm.Invite(inviter, target); err != nil {
		t.Fatalf("Invite: %v", err)
	}
	gm.Tick(time.Now().Add(time.Minute))
	if _, err := gm.Accept(target); err != ErrGroupNoInvite {
		t.Fatalf("expired invite accept err = %v, want ErrGroupNoInvite", err)
	}
}

func TestDamageGate_SameGroupRejected(t *testing.T) {
	dh := NewDamageHandler()
	attacker := &network.PlayerSession{SessionID: 9001, Faction: "stalker", Health: 100}
	target := &network.PlayerSession{SessionID: 9002, Faction: "bandit", Health: 100, Position: [3]float32{10, 0, 0}}

	gm := NewGroupManager(4, time.Minute)
	if err := gm.Invite(attacker, target); err != nil {
		t.Fatalf("Invite: %v", err)
	}
	if _, err := gm.Accept(target); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	dh.SetGroupManager(gm)

	dmg := &protocol.DamageNotify{AttackerID: 9001, TargetID: 9002, Damage: 25.0}
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
	if valid || applied != 0 {
		t.Fatalf("same-group damage applied: valid=%v applied=%v", valid, applied)
	}
	if reason != "same group friendly fire" {
		t.Fatalf("reason = %q, want same group friendly fire", reason)
	}
	if target.Health != 100.0 {
		t.Fatalf("target health = %f, want 100", target.Health)
	}
}

func TestDamageGate_SameFactionRejected(t *testing.T) {
	dh := NewDamageHandler()
	for _, pair := range [][2]string{
		{"stalker", "stalker"},
		{"stalker", "actor_stalker"},
		{"actor_dolg", "DOLG"},
	} {
		attacker := &network.PlayerSession{SessionID: 9101, Faction: pair[0], Health: 100}
		target := &network.PlayerSession{SessionID: 9102, Faction: pair[1], Health: 100, Position: [3]float32{10, 0, 0}}
		dmg := &protocol.DamageNotify{AttackerID: 9101, TargetID: 9102, Damage: 25.0}
		applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
		if valid || applied != 0 || reason != "same faction friendly fire" {
			t.Fatalf("%v vs %v: valid=%v applied=%v reason=%q, want same faction friendly fire",
				pair[0], pair[1], valid, applied, reason)
		}
	}
}

func TestDamageGate_DifferentFactionAllowed(t *testing.T) {
	dh := NewDamageHandler()
	attacker := &network.PlayerSession{SessionID: 9201, Faction: "stalker", Health: 100}
	target := &network.PlayerSession{SessionID: 9202, Faction: "monolith", Health: 100, Position: [3]float32{10, 0, 0}}

	dmg := &protocol.DamageNotify{AttackerID: 9201, TargetID: 9202, Damage: 25.0}
	applied, valid, reason := dh.ValidateAndApplyDamage(attacker, target, dmg)
	if !valid || applied != 25.0 || reason != "" {
		t.Fatalf("different-faction damage: valid=%v applied=%v reason=%q", valid, applied, reason)
	}
	if target.Health != 75.0 {
		t.Fatalf("target health = %f, want 75", target.Health)
	}
}

func TestFactionRelationsDefaults(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"stalker", "bandit", -2000},
		{"bandit", "stalker", -2000},
		{"actor_stalker", "dolg", 0},
		{"monolith", "greh", 300},
		{"greh", "monolith", 300},
		{"army", "monolith", -2000},
		{"unknown_faction", "stalker", 0},
		{"stalker", "stalker", 0},
	}
	for _, tc := range cases {
		if got := RelationBetween(tc.a, tc.b); got != tc.want {
			t.Errorf("RelationBetween(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	if !sameFaction("actor_stalker", "STALKER") {
		t.Error("sameFaction did not normalize actor_ prefix and case")
	}
	if sameFaction("", "") {
		t.Error("empty factions must not count as same-faction")
	}
	if sameFaction("stalker", "bandit") {
		t.Error("different factions reported as same")
	}
}

func TestEntityEnterV3_132BytesWithName(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)

	ghost := connectMember(t, s, 46400, "uuid-enter-v3", "Ghost")
	ghost.sess.Lock()
	ghost.sess.Faction = "freedom"
	ghost.sess.Gvid = 0x0042
	ghost.sess.Unlock()

	peer := &network.PlayerSession{
		SessionID:    9999,
		AccountID:    "uuid-enter-v3-peer",
		Name:         "Peer",
		UDPAddr:      &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 46401},
		CurrentLevel: "l01_escape",
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(peer)
	sink.Reset()

	var pv protocol.PlayerVisualPayload
	copy(pv.Visual[:], `actors\stalker_neutral\stalker_neutral_1`)
	s.HandlePacket(buildTestPacket(t, protocol.OpPlayerVisual, 2, protocol.FlagReliable, pv), ghost.addr)

	raw := findOpcodePacket(t, sink, protocol.OpEntityEnterAoI)
	if raw == nil {
		t.Fatal("peer did not receive ENTITY_ENTER_AOI")
	}
	r := bytes.NewReader(raw)
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.PayloadLength != 132 {
		t.Fatalf("ENTITY_ENTER payload = %d bytes, want 132", hdr.PayloadLength)
	}
	var enter protocol.EntityEnterAoI
	if err := binary.Read(r, binary.LittleEndian, &enter); err != nil {
		t.Fatalf("EntityEnterAoI read: %v", err)
	}
	if enter.EntityID != sessionIDOf(ghost.sess) || enter.EntityType != 1 {
		t.Errorf("enter identity = %d/%d, want %d/1", enter.EntityID, enter.EntityType, sessionIDOf(ghost.sess))
	}
	if got := trimField(enter.Name[:]); got != "Ghost" {
		t.Errorf("enter name = %q, want Ghost", got)
	}
	if got := trimField(enter.Faction[:]); got != "freedom" {
		t.Errorf("enter faction = %q, want freedom", got)
	}
	if enter.Gvid != 0x0042 {
		t.Errorf("enter gvid = 0x%04X, want 0x0042", enter.Gvid)
	}
}
