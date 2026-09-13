package game

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

func TestEmission_ConstructorAndDurations(t *testing.T) {
	// 1. Default constructor
	orch := NewEmissionOrchestrator()
	if orch.CurrentState() != EmissionDormant {
		t.Fatalf("expected initial state EmissionDormant, got %v", orch.CurrentState())
	}
	if orch.DormantDuration() != DefaultDormantDuration {
		t.Fatalf("expected default dormant duration %v, got %v", DefaultDormantDuration, orch.DormantDuration())
	}
	timeUntil := orch.TimeUntilNext()
	if timeUntil <= 0 || timeUntil > DefaultDormantDuration {
		t.Fatalf("expected TimeUntilNext between 0 and %v, got %v", DefaultDormantDuration, timeUntil)
	}

	// 2. Custom duration in constructor
	customDormant := 15 * time.Minute
	orchCustom := NewEmissionOrchestrator(customDormant)
	if orchCustom.CurrentState() != EmissionDormant {
		t.Fatalf("expected initial state EmissionDormant, got %v", orchCustom.CurrentState())
	}
	if orchCustom.DormantDuration() != customDormant {
		t.Fatalf("expected custom dormant duration %v, got %v", customDormant, orchCustom.DormantDuration())
	}
	timeUntilCustom := orchCustom.TimeUntilNext()
	if timeUntilCustom <= 0 || timeUntilCustom > customDormant {
		t.Fatalf("expected TimeUntilNext between 0 and %v, got %v", customDormant, timeUntilCustom)
	}

	// 3. Fallback when 0 or negative duration is passed to constructor
	orchZero := NewEmissionOrchestrator(0)
	if orchZero.DormantDuration() != DefaultDormantDuration {
		t.Fatalf("expected default dormant duration for 0, got %v", orchZero.DormantDuration())
	}

	// 4. SetDormantDuration update
	newDormant := 5 * time.Minute
	orch.SetDormantDuration(newDormant)
	if orch.DormantDuration() != newDormant {
		t.Fatalf("expected updated dormant duration %v, got %v", newDormant, orch.DormantDuration())
	}
	timeUntilUpdated := orch.TimeUntilNext()
	if timeUntilUpdated <= 0 || timeUntilUpdated > newDormant {
		t.Fatalf("expected TimeUntilNext between 0 and %v, got %v", newDormant, timeUntilUpdated)
	}

	// 5. Negative duration in SetDormantDuration ignored
	orch.SetDormantDuration(-1 * time.Minute)
	if orch.DormantDuration() != newDormant {
		t.Fatalf("negative dormant duration should be ignored, got %v", orch.DormantDuration())
	}

	// 6. Phase duration getters & setters
	if orch.WarningDuration() != DefaultWarningDuration {
		t.Fatalf("expected warning duration %v, got %v", DefaultWarningDuration, orch.WarningDuration())
	}
	if orch.ActiveDuration() != DefaultActiveDuration {
		t.Fatalf("expected active duration %v, got %v", DefaultActiveDuration, orch.ActiveDuration())
	}
	if orch.ClearDuration() != DefaultClearDuration {
		t.Fatalf("expected clear duration %v, got %v", DefaultClearDuration, orch.ClearDuration())
	}

	orch.SetWarningDuration(45 * time.Second)
	orch.SetActiveDuration(90 * time.Second)
	orch.SetClearDuration(20 * time.Second)

	if orch.WarningDuration() != 45*time.Second {
		t.Fatalf("expected warning duration 45s, got %v", orch.WarningDuration())
	}
	if orch.ActiveDuration() != 90*time.Second {
		t.Fatalf("expected active duration 90s, got %v", orch.ActiveDuration())
	}
	if orch.ClearDuration() != 20*time.Second {
		t.Fatalf("expected clear duration 20s, got %v", orch.ClearDuration())
	}
}

func TestEmission_StateProgression(t *testing.T) {
	// Durations for predictable stepping:
	// Dormant: 10s, Warning: 5s, Active: 8s, Clear: 3s
	orch := NewEmissionOrchestrator(10 * time.Second)
	orch.SetWarningDuration(5 * time.Second)
	orch.SetActiveDuration(8 * time.Second)
	orch.SetClearDuration(3 * time.Second)

	baseTime := time.Now()

	// Initial phase: Dormant
	if orch.CurrentState() != EmissionDormant {
		t.Fatalf("expected initial state EmissionDormant, got %v", orch.CurrentState())
	}

	// Tick before dormant expires: should remain Dormant
	orch.Tick(baseTime.Add(5*time.Second), nil, nil, nil)
	if orch.CurrentState() != EmissionDormant {
		t.Fatalf("expected Dormant before timeout, got %v", orch.CurrentState())
	}

	// Advance past dormant duration (11s >= 10s): Dormant -> Warning
	t1 := baseTime.Add(11 * time.Second)
	orch.Tick(t1, nil, nil, nil)
	if orch.CurrentState() != EmissionWarning {
		t.Fatalf("expected EmissionWarning, got %v", orch.CurrentState())
	}

	// Tick before warning expires (3s into 5s warning): should remain Warning
	orch.Tick(t1.Add(3*time.Second), nil, nil, nil)
	if orch.CurrentState() != EmissionWarning {
		t.Fatalf("expected EmissionWarning before timeout, got %v", orch.CurrentState())
	}

	// Advance past warning duration (6s >= 5s): Warning -> Active
	t2 := t1.Add(6 * time.Second)
	orch.Tick(t2, nil, nil, nil)
	if orch.CurrentState() != EmissionActive {
		t.Fatalf("expected EmissionActive, got %v", orch.CurrentState())
	}

	// Tick before active expires (4s into 8s active): should remain Active
	orch.Tick(t2.Add(4*time.Second), nil, nil, nil)
	if orch.CurrentState() != EmissionActive {
		t.Fatalf("expected EmissionActive before timeout, got %v", orch.CurrentState())
	}

	// Advance past active duration (9s >= 8s): Active -> Clear
	t3 := t2.Add(9 * time.Second)
	orch.Tick(t3, nil, nil, nil)
	if orch.CurrentState() != EmissionClear {
		t.Fatalf("expected EmissionClear, got %v", orch.CurrentState())
	}

	// Tick before clear expires (1s into 3s clear): should remain Clear
	orch.Tick(t3.Add(1*time.Second), nil, nil, nil)
	if orch.CurrentState() != EmissionClear {
		t.Fatalf("expected EmissionClear before timeout, got %v", orch.CurrentState())
	}

	// Advance past clear duration (4s >= 3s): Clear -> Dormant (loop complete)
	t4 := t3.Add(4 * time.Second)
	orch.Tick(t4, nil, nil, nil)
	if orch.CurrentState() != EmissionDormant {
		t.Fatalf("expected EmissionDormant after full cycle, got %v", orch.CurrentState())
	}
}

func TestEmission_TimeUntilNextPassed(t *testing.T) {
	orch := NewEmissionOrchestrator(5 * time.Second)
	orch.mu.Lock()
	orch.nextTick = time.Now().Add(-10 * time.Second)
	orch.mu.Unlock()

	rem := orch.TimeUntilNext()
	if rem != 0 {
		t.Fatalf("expected 0 for past nextTick, got %v", rem)
	}
}

func TestEmission_ForceEmission(t *testing.T) {
	// 1. Force from Dormant
	orch := NewEmissionOrchestrator(30 * time.Minute)
	if orch.CurrentState() != EmissionDormant {
		t.Fatalf("expected initial Dormant, got %v", orch.CurrentState())
	}

	orch.ForceEmission()
	if orch.CurrentState() != EmissionWarning {
		t.Fatalf("expected EmissionWarning immediately after ForceEmission, got %v", orch.CurrentState())
	}
	rem := orch.TimeUntilNext()
	if rem <= 0 || rem > DefaultWarningDuration {
		t.Fatalf("expected TimeUntilNext between 0 and %v, got %v", DefaultWarningDuration, rem)
	}

	// 2. Force from Active
	orch.mu.Lock()
	orch.state = EmissionActive
	orch.mu.Unlock()
	if orch.CurrentState() != EmissionActive {
		t.Fatalf("expected Active, got %v", orch.CurrentState())
	}
	orch.ForceEmission()
	if orch.CurrentState() != EmissionWarning {
		t.Fatalf("expected EmissionWarning after ForceEmission from Active, got %v", orch.CurrentState())
	}

	// 3. Force from Clear
	orch.mu.Lock()
	orch.state = EmissionClear
	orch.mu.Unlock()
	if orch.CurrentState() != EmissionClear {
		t.Fatalf("expected Clear, got %v", orch.CurrentState())
	}
	orch.ForceEmission()
	if orch.CurrentState() != EmissionWarning {
		t.Fatalf("expected EmissionWarning after ForceEmission from Clear, got %v", orch.CurrentState())
	}
}

func TestEmission_BroadcastAndPacketSerialization(t *testing.T) {
	// 1. Setup real UDP listener as mock client
	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("failed to listen UDP: %v", err)
	}
	defer clientConn.Close()
	clientAddr := clientConn.LocalAddr().(*net.UDPAddr)

	// 2. Setup server UDPListener
	udp, err := network.NewUDPListener(0, func([]byte, *net.UDPAddr) {})
	if err != nil {
		t.Fatalf("failed to create UDPListener: %v", err)
	}
	defer udp.Close()

	// 3. Setup SessionManager with client session
	sessions := network.NewSessionManager()
	sess := &network.PlayerSession{
		SessionID: 42,
		UDPAddr:   clientAddr,
	}
	sessions.AddSession(sess)

	// 4. Test direct broadcast packet generation and serialization
	orch := NewEmissionOrchestrator()
	var seq atomic.Uint32
	timerSec := uint32(1800)

	orch.broadcast(sessions, udp, &seq, timerSec)

	if seq.Load() != 1 {
		t.Fatalf("expected seq to be 1, got %d", seq.Load())
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := clientConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("failed to read UDP packet: %v", err)
	}

	// PacketHeader (12 bytes) + WorldEventPayload (6 bytes) = 18 bytes
	if n != 18 {
		t.Fatalf("expected packet length 18, got %d", n)
	}

	hdr, err := protocol.ReadHeader(bytes.NewReader(buf[:12]))
	if err != nil {
		t.Fatalf("failed to parse header: %v", err)
	}

	if hdr.Magic != protocol.HeaderMagic {
		t.Fatalf("expected magic 0x%04X, got 0x%04X", protocol.HeaderMagic, hdr.Magic)
	}
	if hdr.Protocol != protocol.ProtocolVer {
		t.Fatalf("expected protocol 0x%02X, got 0x%02X", protocol.ProtocolVer, hdr.Protocol)
	}
	if hdr.FlagsChannel != protocol.FlagReliable {
		t.Fatalf("expected flags 0x%02X, got 0x%02X", protocol.FlagReliable, hdr.FlagsChannel)
	}
	if hdr.SequenceNum != 1 {
		t.Fatalf("expected sequence 1, got %d", hdr.SequenceNum)
	}
	if hdr.Opcode != protocol.OpWorldEvent {
		t.Fatalf("expected opcode 0x%04X, got 0x%04X", protocol.OpWorldEvent, hdr.Opcode)
	}
	if hdr.PayloadLength != 6 {
		t.Fatalf("expected payload length 6, got %d", hdr.PayloadLength)
	}

	var payload protocol.WorldEventPayload
	if err := binary.Read(bytes.NewReader(buf[12:n]), binary.LittleEndian, &payload); err != nil {
		t.Fatalf("failed to parse payload: %v", err)
	}

	if payload.EventType != 0 {
		t.Fatalf("expected EventType 0 (Emission), got %d", payload.EventType)
	}
	if payload.State != uint8(EmissionDormant) {
		t.Fatalf("expected State %d, got %d", EmissionDormant, payload.State)
	}
	if payload.Timer != timerSec {
		t.Fatalf("expected Timer %d, got %d", timerSec, payload.Timer)
	}

	// 5. Test Tick state transition broadcast
	orch.SetDormantDuration(1 * time.Second)
	orch.SetWarningDuration(60 * time.Second)
	// Advance time past dormant to trigger transition to Warning
	orch.Tick(time.Now().Add(2*time.Second), sessions, udp, &seq)

	if seq.Load() != 2 {
		t.Fatalf("expected seq to be 2 after transition tick, got %d", seq.Load())
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n2, _, err := clientConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("failed to read second UDP packet: %v", err)
	}
	if n2 != 18 {
		t.Fatalf("expected packet length 18, got %d", n2)
	}

	hdr2, err := protocol.ReadHeader(bytes.NewReader(buf[:12]))
	if err != nil {
		t.Fatalf("failed to parse second header: %v", err)
	}
	if hdr2.SequenceNum != 2 {
		t.Fatalf("expected sequence 2, got %d", hdr2.SequenceNum)
	}

	var payload2 protocol.WorldEventPayload
	if err := binary.Read(bytes.NewReader(buf[12:n2]), binary.LittleEndian, &payload2); err != nil {
		t.Fatalf("failed to parse second payload: %v", err)
	}
	if payload2.State != uint8(EmissionWarning) {
		t.Fatalf("expected State %d (Warning), got %d", EmissionWarning, payload2.State)
	}
	if payload2.Timer != 60 {
		t.Fatalf("expected Timer 60, got %d", payload2.Timer)
	}

	// 6. Test ForceEmission broadcast on subsequent Tick
	orch.ForceEmission()
	orch.Tick(time.Now(), sessions, udp, &seq)

	if seq.Load() != 3 {
		t.Fatalf("expected seq to be 3 after force emission tick, got %d", seq.Load())
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n3, _, err := clientConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("failed to read third UDP packet: %v", err)
	}
	var payload3 protocol.WorldEventPayload
	_ = binary.Read(bytes.NewReader(buf[12:n3]), binary.LittleEndian, &payload3)
	if payload3.State != uint8(EmissionWarning) {
		t.Fatalf("expected State %d (Warning), got %d", EmissionWarning, payload3.State)
	}

	// 7. Test nil safety: should gracefully skip without panic
	orch.Tick(time.Now(), nil, nil, nil)
	orch.broadcast(nil, nil, nil, 60)
	orch.broadcast(sessions, nil, nil, 60)
	orch.broadcast(sessions, udp, nil, 60)

	// Session with nil UDPAddr should be skipped without panic
	nilAddrSess := &network.PlayerSession{
		SessionID: 999,
		UDPAddr:   nil,
	}
	sessions.AddSession(nilAddrSess)
	seqBefore := seq.Load()
	orch.broadcast(sessions, udp, &seq, 10)
	// seq should only increment by 1 (for session 42), not for session 999
	if seq.Load() != seqBefore+1 {
		t.Fatalf("expected seq to increment by 1, got %d -> %d", seqBefore, seq.Load())
	}
}

func TestEmission_ConcurrentRace(t *testing.T) {
	orch := NewEmissionOrchestrator(100 * time.Millisecond)
	orch.SetWarningDuration(50 * time.Millisecond)
	orch.SetActiveDuration(80 * time.Millisecond)
	orch.SetClearDuration(30 * time.Millisecond)

	sessions := network.NewSessionManager()
	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("failed to listen UDP: %v", err)
	}
	defer clientConn.Close()

	sess := &network.PlayerSession{
		SessionID: 1,
		UDPAddr:   clientConn.LocalAddr().(*net.UDPAddr),
	}
	sessions.AddSession(sess)

	udp, err := network.NewUDPListener(0, func([]byte, *net.UDPAddr) {})
	if err != nil {
		t.Fatalf("failed to create UDPListener: %v", err)
	}
	defer udp.Close()

	var seq atomic.Uint32
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Goroutine group 1: Ticker
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					orch.Tick(time.Now(), sessions, udp, &seq)
					time.Sleep(1 * time.Millisecond)
				}
			}
		}()
	}

	// Goroutine group 2: Readers
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = orch.CurrentState()
					_ = orch.TimeUntilNext()
					_ = orch.DormantDuration()
					_ = orch.WarningDuration()
					_ = orch.ActiveDuration()
					_ = orch.ClearDuration()
				}
			}
		}()
	}

	// Goroutine group 3: Mutators
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					orch.ForceEmission()
					orch.SetDormantDuration(200 * time.Millisecond)
					orch.SetWarningDuration(50 * time.Millisecond)
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}

	// Goroutine group 4: Broadcaster
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				orch.broadcast(sessions, udp, &seq, 10)
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}
