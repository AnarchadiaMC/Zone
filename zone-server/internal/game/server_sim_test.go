package game

import (
	"bytes"
	"encoding/binary"
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

func TestSim_EmissionOrchestratorTicked(t *testing.T) {
	s, _, _ := setupTestServerWithDB(t)

	if s.EmissionMgr() == nil {
		t.Fatal("expected EmissionMgr to be initialized and non-nil")
	}
	if s.EmissionMgr().CurrentState() != EmissionDormant {
		t.Fatalf("expected initial emission state to be EmissionDormant, got %v", s.EmissionMgr().CurrentState())
	}

	// Setup mock client UDP listener
	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("failed to listen UDP: %v", err)
	}
	defer clientConn.Close()
	clientAddr := clientConn.LocalAddr().(*net.UDPAddr)

	// Register a connected player session with the mock client address
	sess := &network.PlayerSession{
		SessionID:    101,
		AccountID:    "uuid-sim-emission-1",
		UDPAddr:      clientAddr,
		CurrentLevel: "l01_escape",
		Health:       100.0,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)

	// Configure a short dormant duration for testing
	s.EmissionMgr().SetDormantDuration(50 * time.Millisecond)

	// Ticking before duration elapses must not advance emission state
	s.Tick(time.Now())
	if s.EmissionMgr().CurrentState() != EmissionDormant {
		t.Fatalf("expected state to remain EmissionDormant before duration elapses, got %v", s.EmissionMgr().CurrentState())
	}

	// Ticking past dormantDuration advances to EmissionWarning and broadcasts OpWorldEvent (0x0030)
	futureTime := time.Now().Add(100 * time.Millisecond)
	sess.Lock()
	sess.LastSeen = futureTime // Keep session active so stale timeout doesn't evict it
	sess.Unlock()

	s.Tick(futureTime)

	if s.EmissionMgr().CurrentState() != EmissionWarning {
		t.Fatalf("expected state to transition to EmissionWarning, got %v", s.EmissionMgr().CurrentState())
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := clientConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("failed to receive OpWorldEvent UDP packet: %v", err)
	}

	hdr, err := protocol.ReadHeader(bytes.NewReader(buf[:12]))
	if err != nil {
		t.Fatalf("failed to read packet header: %v", err)
	}
	if hdr.Opcode != protocol.OpWorldEvent {
		t.Fatalf("expected Opcode OpWorldEvent (0x0030), got 0x%04X", hdr.Opcode)
	}

	var event protocol.WorldEventPayload
	if err := binary.Read(bytes.NewReader(buf[12:n]), binary.LittleEndian, &event); err != nil {
		t.Fatalf("failed to decode WorldEventPayload: %v", err)
	}
	if event.EventType != 0 { // 0 = Emission
		t.Errorf("expected EventType 0 (Emission), got %d", event.EventType)
	}
	if event.State != uint8(EmissionWarning) {
		t.Errorf("expected State %d (Warning), got %d", EmissionWarning, event.State)
	}

	// Ticking past warning duration (default 60s) advances to EmissionActive
	activeTime := futureTime.Add(70 * time.Second)
	sess.Lock()
	sess.LastSeen = activeTime
	sess.Unlock()

	s.Tick(activeTime)
	if s.EmissionMgr().CurrentState() != EmissionActive {
		t.Fatalf("expected state to transition to EmissionActive, got %v", s.EmissionMgr().CurrentState())
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	nActive, _, err := clientConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("failed to receive active wave OpWorldEvent UDP packet: %v", err)
	}
	var eventActive protocol.WorldEventPayload
	_ = binary.Read(bytes.NewReader(buf[12:nActive]), binary.LittleEndian, &eventActive)
	if eventActive.State != uint8(EmissionActive) {
		t.Errorf("expected State %d (Active), got %d", EmissionActive, eventActive.State)
	}
}

func TestSim_AISquadTickedAndActionBroadcast(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	// Register a connected player session
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40002}
	sess := &network.PlayerSession{
		SessionID:    201,
		AccountID:    "uuid-sim-ai-1",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		Health:       100.0,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)

	// Spawn an AI squad
	startX, startY, startZ := float32(100.0), float32(5.0), float32(200.0)
	sq := s.squads.SpawnSquad("l01_escape", startX, startY, startZ, "stalker")
	if sq == nil {
		t.Fatal("failed to spawn AI squad")
	}
	if sq.State != ai.AIStatePatrol {
		t.Fatalf("expected initial squad state AIStatePatrol, got %v", sq.State)
	}

	sink.Reset()

	// Ticking server must tick squads and broadcast OpAiAction (OpAIActionEvent 0x0070)
	s.Tick(time.Now())

	if sink.PacketCount() == 0 {
		t.Fatal("expected OpAiAction packet in sink after server.Tick")
	}

	r := bytes.NewReader(sink.LastPacket())
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("failed to read packet header: %v", err)
	}
	if hdr.Opcode != OpAiAction {
		t.Fatalf("expected Opcode OpAiAction (0x0070), got 0x%04X", hdr.Opcode)
	}

	var action protocol.AIActionPayload
	if err := binary.Read(r, binary.LittleEndian, &action); err != nil {
		t.Fatalf("failed to decode AIActionPayload: %v", err)
	}
	if action.EntityID != sq.ID {
		t.Errorf("expected EntityID %d, got %d", sq.ID, action.EntityID)
	}
	if action.Action != uint8(ai.AIStatePatrol) {
		t.Errorf("expected Action %d (Patrol), got %d", ai.AIStatePatrol, action.Action)
	}
}

func TestSim_AckQueueTicked(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	sink.Reset()

	// Register a player session
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40003}
	sess := &network.PlayerSession{
		SessionID:    301,
		AccountID:    "uuid-sim-ack-1",
		UDPAddr:      addr,
		CurrentLevel: "l01_escape",
		Health:       100.0,
		LastSeen:     time.Now(),
	}
	s.sessions.AddSession(sess)

	// Send a reliable packet to session (enqueues into ackQueue)
	msg := protocol.NewChatText(301, "reliable test message")
	s.SendToSession(sess, protocol.OpChatText, protocol.FlagReliable, msg)

	if s.ackQueue.Len() != 1 {
		t.Fatalf("expected 1 unacknowledged packet in ackQueue, got %d", s.ackQueue.Len())
	}
	if sink.PacketCount() != 1 {
		t.Fatalf("expected initial packet in sink, got %d", sink.PacketCount())
	}

	// Extract sequence number from transmitted packet
	r := bytes.NewReader(sink.LastPacket())
	hdr, err := protocol.ReadHeader(r)
	if err != nil {
		t.Fatalf("failed to read packet header: %v", err)
	}
	seq := hdr.SequenceNum
	if seq == 0 {
		t.Fatal("expected non-zero sequence number on reliable packet")
	}

	// Advance time past the default retransmit interval (500ms)
	retransmitTime := time.Now().Add(600 * time.Millisecond)
	s.Tick(retransmitTime)

	// Verify packet was retransmitted by ackQueue.Tick(now)
	if sink.PacketCount() < 2 {
		t.Fatalf("expected packet retransmission in sink, got packet count %d", sink.PacketCount())
	}

	// Client sends incoming OpAck packet acknowledging seq
	ackPkt := buildTestPacket(t, OpAck, seq, protocol.FlagReliable, nil)
	s.HandlePacket(ackPkt, addr)

	// Verify packet acknowledged and removed from ackQueue
	if s.ackQueue.Len() != 0 {
		t.Fatalf("expected ackQueue to be empty after OpAck acknowledgment, got %d entries", s.ackQueue.Len())
	}

	// Verify subsequent ticks do not retransmit acknowledged packet
	countAfterAck := sink.PacketCount()
	s.Tick(retransmitTime.Add(1 * time.Second))
	if sink.PacketCount() != countAfterAck {
		t.Errorf("expected no further retransmissions after ACK, got %d packets", sink.PacketCount())
	}

	// Also verify incoming ACK packet with sequence in binary payload
	s.SendToSession(sess, protocol.OpChatText, protocol.FlagReliable, msg)
	if s.ackQueue.Len() != 1 {
		t.Fatalf("expected 1 unacknowledged packet after second reliable send")
	}
	r2 := bytes.NewReader(sink.LastPacket())
	hdr2, _ := protocol.ReadHeader(r2)
	seq2 := hdr2.SequenceNum

	ackPayloadPkt := buildTestPacket(t, OpAck, 0, protocol.FlagReliable, seq2)
	s.HandlePacket(ackPayloadPkt, addr)
	if s.ackQueue.Len() != 0 {
		t.Fatalf("expected ackQueue to be empty after payload seq ACK, got %d entries", s.ackQueue.Len())
	}
}

func TestSim_EmissionIntervalConfig(t *testing.T) {
	cfg := &config.Config{
		EmissionIntervalMin: 45,
	}
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	db.RawDB().SetMaxOpenConns(1)

	server := NewServer(cfg, db, zap.NewNop())
	if server.EmissionMgr() == nil {
		t.Fatal("expected EmissionMgr to be initialized")
	}
	if server.EmissionMgr().DormantDuration() != 45*time.Minute {
		t.Errorf("expected 45m dormant duration from config, got %v", server.EmissionMgr().DormantDuration())
	}
}
