package game

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// Per-session isolation regressions: every mutable cache/window must be keyed
// by the session/account and must never serve one player's cached result or
// consume another player's rate budget.

func provisionReturningChar(t *testing.T, s *Server, uuid, nick, loadout string) {
	t.Helper()
	if err := s.db.AutoProvision(uuid, "hwid-"+uuid, nick); err != nil {
		t.Fatalf("AutoProvision(%s): %v", uuid, err)
	}
	if err := s.db.CreateCharacter(uuid, "stalker", loadout, 1000); err != nil {
		t.Fatalf("CreateCharacter(%s): %v", uuid, err)
	}
}

// A's inventory sync must carry A's rows and must not advance B's per-level
// sync marker (nor deliver B's rows to A).
func TestIsolation_InventorySyncIsPerSession(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	provisionReturningChar(t, s, "uuid-sync-a", "SyncA", "medkit")
	provisionReturningChar(t, s, "uuid-sync-b", "SyncB", "bandage")

	addrA := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45101}
	addrB := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 45102}
	doJoinHandshake(t, s, addrA, "uuid-sync-a", "SyncA")
	doJoinHandshake(t, s, addrB, "uuid-sync-b", "SyncB")

	sessA := s.sessions.GetByAddr(addrA.String())
	sessB := s.sessions.GetByAddr(addrB.String())
	if sessA == nil || sessB == nil {
		t.Fatal("sessions missing after handshake")
	}
	for _, sess := range []*network.PlayerSession{sessA, sessB} {
		sess.Lock()
		sess.InventorySyncedLevel = ""
		sess.Unlock()
	}

	sink.Reset()
	s.syncInventoryForLevel(sessA)
	syncs := packetsByOpcode(sink, protocol.OpInventorySync)
	if len(syncs) != 1 {
		t.Fatalf("A sync packets = %d, want 1", len(syncs))
	}
	items := readInventorySyncItems(t, syncs[0])
	if len(items) != 1 || string(bytes.TrimRight(items[0].Section[:], "\x00")) != "medkit" {
		t.Fatalf("A sync items = %+v, want A's medkit only", items)
	}
	sessB.Lock()
	syncedB := sessB.InventorySyncedLevel
	sessB.Unlock()
	if syncedB != "" {
		t.Fatalf("B sync marker advanced while A synced: %q", syncedB)
	}

	sink.Reset()
	s.syncInventoryForLevel(sessB)
	syncs = packetsByOpcode(sink, protocol.OpInventorySync)
	if len(syncs) != 1 {
		t.Fatalf("B sync packets = %d, want 1", len(syncs))
	}
	items = readInventorySyncItems(t, syncs[0])
	if len(items) != 1 || string(bytes.TrimRight(items[0].Section[:], "\x00")) != "bandage" {
		t.Fatalf("B sync items = %+v, want B's bandage only", items)
	}
	sessA.Lock()
	syncedA := sessA.InventorySyncedLevel
	sessA.Unlock()
	sessB.Lock()
	syncedB = sessB.InventorySyncedLevel
	sessB.Unlock()
	if syncedA != "l01_escape" || syncedB != "l01_escape" {
		t.Fatalf("sync markers = %q / %q, want both l01_escape", syncedA, syncedB)
	}
}

// The ActionID LRU, monotonic floor and token window are per session: B can
// neither hit A's cache nor inherit A's exhausted rate budget, and clearing A
// leaves B and the floor reset for A intact.
func TestIsolation_ItemLedgerPerSession(t *testing.T) {
	ledger := NewItemLedger(5)
	now := time.Now()

	for i := 0; i < 5; i++ {
		if !ledger.AllowAction(1, now) {
			t.Fatalf("session 1 action %d refused within budget", i+1)
		}
	}
	if ledger.AllowAction(1, now) {
		t.Fatal("session 1 exceeded budget without refusal")
	}
	if !ledger.AllowAction(2, now) {
		t.Fatal("session 2 starved by session 1's exhausted budget")
	}

	cached := protocol.ItemUpdatePacket{ActionID: 77, Result: protocol.ItemResultOK, Count: 3}
	ledger.Record(1, 77, cached)
	if _, ok := ledger.Lookup(1, 77); !ok {
		t.Fatal("own cached ActionID not found")
	}
	if _, ok := ledger.Lookup(2, 77); ok {
		t.Fatal("session 2 read session 1's cached ItemUpdate")
	}
	if ledger.IsStale(2, 77) {
		t.Fatal("session 2 inherited session 1's monotonic ActionID floor")
	}

	ledger.Remove(1)
	if _, ok := ledger.Lookup(1, 77); ok {
		t.Fatal("cached ActionID survived Remove")
	}
	if !ledger.AllowAction(1, now) {
		t.Fatal("rate budget not reset after Remove")
	}
	if _, ok := ledger.Lookup(2, 77); ok {
		t.Fatal("removing session 1 dropped session 2's cache state")
	}
}

// Chat windows are per session: one player's flood must not silence another.
func TestIsolation_ChatRateWindowPerSession(t *testing.T) {
	now := time.Now()
	a := &network.PlayerSession{SessionID: 1}
	b := &network.PlayerSession{SessionID: 2}

	for i := 0; i < 5; i++ {
		if !a.AllowChat(now) {
			t.Fatalf("A chat %d refused within budget", i+1)
		}
	}
	if a.AllowChat(now) {
		t.Fatal("A exceeded its 5/5s chat window")
	}
	if !b.AllowChat(now) {
		t.Fatal("B chat refused because A exhausted A's window")
	}
}

// A server-level flood accepted only 5 of A's messages but B's single message
// still broadcasts.
func TestIsolation_ChatFloodDoesNotStarveOtherSession(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	provisionReturningChar(t, s, "uuid-flood-a", "FloodA", "")
	provisionReturningChar(t, s, "uuid-flood-b", "FloodB", "")

	alpha := connectMember(t, s, 45201, "uuid-flood-a", "FloodA")
	bravo := connectMember(t, s, 45202, "uuid-flood-b", "FloodB")
	idA := sessionIDOf(alpha.sess)
	idB := sessionIDOf(bravo.sess)
	sink.Reset()

	for i := 0; i < 6; i++ {
		sendChatFrom(t, s, alpha.addr, uint32(10+i), idA, "spam")
	}
	sendChatFrom(t, s, bravo.addr, 20, idB, "hello")

	senders := map[uint32]int{}
	for _, raw := range packetsByOpcode(sink, protocol.OpChatText) {
		var pkt protocol.ChatText
		if err := binary.Read(bytes.NewReader(packetPayload(t, raw)), binary.LittleEndian, &pkt); err != nil {
			t.Fatalf("decode chat: %v", err)
		}
		senders[pkt.SenderID]++
	}
	// Each accepted message is fanned out to the two same-level sessions
	// (sender included): A's 5 accepted = 10 packets, B's 1 = 2 packets.
	if senders[idA] != 10 {
		t.Fatalf("A broadcast packets = %d, want 10 (5 accepted x 2 recipients)", senders[idA])
	}
	if senders[idB] != 2 {
		t.Fatalf("B broadcast packets = %d, want 2 (starved by A's flood)", senders[idB])
	}
}

// Every per-session container is dropped on kick: item ledger, damage budget,
// damage-reject audit throttle, AI visibility and pending invites. A second
// session's identical state must survive.
func TestIsolation_SessionCleanupClearsOnlyTargetSession(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	va := ledgerSession(t, s, 9101, 46101, "uuid-clean-a", "l01_escape", [3]float32{})
	vb := ledgerSession(t, s, 9102, 46102, "uuid-clean-b", "l01_escape", [3]float32{})

	// Populate A's per-session state and a mirror entry for B.
	s.itemLedger.Record(va.SessionID, 55, protocol.ItemUpdatePacket{ActionID: 55})
	s.itemLedger.Record(vb.SessionID, 55, protocol.ItemUpdatePacket{ActionID: 55})
	s.auditDamageRejected(va.SessionID, "test")
	if charged, ok := s.damageHandler.chargeBudget(va.SessionID, 300); !ok || charged != 300 {
		t.Fatalf("seed damage budget = %v/%v", charged, ok)
	}
	s.aiRepl.markVisible(va.SessionID, 42)
	if err := s.groups.Invite(va, vb); err != nil {
		t.Fatalf("Invite: %v", err)
	}

	if !s.KickSession(va.SessionID) {
		t.Fatal("KickSession returned false")
	}

	if _, ok := s.itemLedger.Lookup(va.SessionID, 55); ok {
		t.Fatal("item ledger entry survived kick")
	}
	s.damageAuditMu.Lock()
	_, rejectAudit := s.lastDamageRejectAudit[va.SessionID]
	s.damageAuditMu.Unlock()
	if rejectAudit {
		t.Fatal("damage-reject audit throttle survived kick")
	}
	if charged, ok := s.damageHandler.chargeBudget(va.SessionID, 400); !ok || charged != 400 {
		t.Fatalf("damage budget after kick = %v/%v, want 400/true (cleared)", charged, ok)
	}
	if ids := s.aiRepl.visibleIDs(va.SessionID); len(ids) != 0 {
		t.Fatalf("AI visibility survived kick: %v", ids)
	}
	if inv := s.groups.InviteFor(vb.SessionID); inv != nil {
		t.Fatal("pending invite from kicked session survived cleanup")
	}

	// B's mirrored state is untouched.
	if _, ok := s.itemLedger.Lookup(vb.SessionID, 55); !ok {
		t.Fatal("session B's ledger entry was cleared by A's kick")
	}
}

// Group membership links friends for combat/chat only; it must never share
// inventory. A's consume/drop never changes B's persisted rows.
func TestIsolation_GroupMembersDoNotShareInventory(t *testing.T) {
	s, _, db := setupTestServerWithDB(t)
	provisionReturningChar(t, s, "uuid-grp-a", "GroupA", "medkit")
	provisionReturningChar(t, s, "uuid-grp-b", "GroupB", "bandage")

	alpha := connectMember(t, s, 46201, "uuid-grp-a", "GroupA")
	bravo := connectMember(t, s, 46202, "uuid-grp-b", "GroupB")
	sendChatCommand(t, s, alpha.sess, 2, "/invite GroupB")
	sendChatCommand(t, s, bravo.sess, 3, "/accept")
	if !s.groups.IsSameGroup(sessionIDOf(alpha.sess), sessionIDOf(bravo.sess)) {
		t.Fatal("test setup: group was not formed")
	}

	mustExec(t, s, "UPDATE character_inventory SET item_count=5 WHERE client_uuid=? AND item_section='medkit'", "uuid-grp-a")

	var consume protocol.ItemActionPacket
	consume.ActionID = 9001
	consume.Action = protocol.ItemActionConsume
	consume.Count = 2
	assembleSection(&consume.Section, "medkit")
	sendItemAction(t, s, alpha.sess, 4, consume)

	var drop protocol.ItemActionPacket
	drop.ActionID = 9002
	drop.Action = protocol.ItemActionDrop
	drop.Count = 1
	drop.X, drop.Y, drop.Z = 0, 0, 0
	assembleSection(&drop.Section, "medkit")
	sendItemAction(t, s, alpha.sess, 5, drop)

	if got, _ := db.AvailableItemCount("uuid-grp-a", "medkit"); got != 2 {
		t.Fatalf("group member A medkit = %d, want 2 after consume+drop", got)
	}
	if got, _ := db.AvailableItemCount("uuid-grp-b", "bandage"); got != 1 {
		t.Fatalf("group member B bandage = %d, want 1 (untouched by A)", got)
	}
	if got, _ := db.AvailableItemCount("uuid-grp-b", "medkit"); got != 0 {
		t.Fatalf("group member B gained A's medkit: %d", got)
	}
}

// One endpoint's ACK may not clear another endpoint's reliable retransmit
// entry, even when it presents the correct global sequence number.
func TestIsolation_ForeignAckDoesNotClearRetransmit(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	provisionReturningChar(t, s, "uuid-ack-a", "AckA", "")
	provisionReturningChar(t, s, "uuid-ack-b", "AckB", "")

	alpha := connectMember(t, s, 46301, "uuid-ack-a", "AckA")
	bravo := connectMember(t, s, 46302, "uuid-ack-b", "AckB")

	// Drop handshake-era reliable entries; isolate one packet for Alpha.
	s.ackQueue.Clear()
	sink.Reset()
	s.SendToSession(alpha.sess, protocol.OpChatText, protocol.FlagReliable, protocol.NewChatText(sessionIDOf(alpha.sess), "pending"))
	if s.ackQueue.Len() != 1 {
		t.Fatalf("ack queue len = %d, want 1", s.ackQueue.Len())
	}
	sink.mu.Lock()
	raw := sink.sent[len(sink.sent)-1]
	sink.mu.Unlock()
	hdr, err := protocol.ReadHeader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}

	foreignAck := buildTestPacket(t, protocol.OpAck, 900000, protocol.FlagReliable, hdr.SequenceNum)
	s.HandlePacket(foreignAck, bravo.addr)
	if s.ackQueue.Len() != 1 {
		t.Fatal("foreign endpoint cleared Alpha's retransmit entry")
	}

	ownAck := buildTestPacket(t, protocol.OpAck, 900001, protocol.FlagReliable, hdr.SequenceNum)
	s.HandlePacket(ownAck, alpha.addr)
	if s.ackQueue.Len() != 0 {
		t.Fatal("owning endpoint's ACK did not clear its entry")
	}
}
