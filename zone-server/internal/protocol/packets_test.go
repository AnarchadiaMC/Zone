package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

// SafezoneStatePayload represents the payload sent for OpSafezoneState (0x0020).
type SafezoneStatePayload struct {
	InSafeZone uint8
}

// LeaveAoIPayload represents a session-level leave notification payload.
type LeaveAoIPayload struct {
	SessionID uint32
}

// EnterAoIPayload represents a session-level enter notification payload.
type EnterAoIPayload struct {
	SessionID uint32
	PosX      float32
	PosY      float32
	PosZ      float32
}

func TestPackets(t *testing.T) {
	t.Run("Write and Read with Payload", func(t *testing.T) {
		var buf bytes.Buffer
		req := HandshakeReq{
			ProtocolVer: ProtocolVer,
		}

		err := WritePacket(&buf, OpHandshakeReq, 1, FlagReliable, req)
		if err != nil {
			t.Fatalf("Failed to write packet: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("Failed to read header: %v", err)
		}

		if hdr.Magic != HeaderMagic {
			t.Errorf("Expected Magic %x, got %x", HeaderMagic, hdr.Magic)
		}
		if hdr.Opcode != OpHandshakeReq {
			t.Errorf("Expected Opcode %d, got %d", OpHandshakeReq, hdr.Opcode)
		}
		if hdr.SequenceNum != 1 {
			t.Errorf("Expected SequenceNum 1, got %d", hdr.SequenceNum)
		}
		if hdr.FlagsChannel != FlagReliable {
			t.Errorf("Expected FlagsChannel %x, got %x", FlagReliable, hdr.FlagsChannel)
		}
		if hdr.PayloadLength == 0 {
			t.Errorf("Expected payload length > 0, got %d", hdr.PayloadLength)
		}
	})

	t.Run("Write and Read with Nil Payload", func(t *testing.T) {
		var buf bytes.Buffer

		err := WritePacket(&buf, OpHeartbeat, 2, FlagUnreliable, nil)
		if err != nil {
			t.Fatalf("Failed to write packet: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("Failed to read header: %v", err)
		}

		if hdr.Opcode != OpHeartbeat {
			t.Errorf("Expected Opcode %d, got %d", OpHeartbeat, hdr.Opcode)
		}
		if hdr.PayloadLength != 0 {
			t.Errorf("Expected payload length 0, got %d", hdr.PayloadLength)
		}
	})

	t.Run("OpHandshakeReq Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		req := HandshakeReq{
			ProtocolVer: ProtocolVer,
		}
		copy(req.UUID[:], "550e8400-e29b-41d4-a716-446655440000")
		copy(req.HWIDHash[:], []byte{0xDE, 0xAD, 0xBE, 0xEF})
		copy(req.Nickname[:], "StalkerMajor")

		const expectedLen = 36 + 4 + 32 + 1 // 73 bytes
		if err := WritePacket(&buf, OpHandshakeReq, 10, FlagReliable, req); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpHandshakeReq {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpHandshakeReq)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded HandshakeReq
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if string(decoded.UUID[:]) != "550e8400-e29b-41d4-a716-446655440000" {
			t.Errorf("UUID mismatch: %s", string(decoded.UUID[:]))
		}
		if decoded.HWIDHash != [4]byte{0xDE, 0xAD, 0xBE, 0xEF} {
			t.Errorf("HWIDHash mismatch: %v", decoded.HWIDHash)
		}
		if string(decoded.Nickname[:12]) != "StalkerMajor" {
			t.Errorf("Nickname mismatch: %s", string(decoded.Nickname[:]))
		}
		if decoded.ProtocolVer != ProtocolVer {
			t.Errorf("ProtocolVer mismatch: got %d, want %d", decoded.ProtocolVer, ProtocolVer)
		}
	})

	t.Run("OpHandshakeRes Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		res := HandshakeRes{
			SessionID: [4]byte{0x01, 0x02, 0x03, 0x04},
			Status:    0,
			SpawnX:    -211.35,
			SpawnY:    -20.25,
			SpawnZ:    -145.80,
			WorldTime: 1716382910,
			EcoTier:   2,
		}

		const expectedLen = 4 + 1 + 4 + 4 + 4 + 8 + 1 // 26 bytes
		if err := WritePacket(&buf, OpHandshakeRes, 11, FlagReliable, res); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpHandshakeRes {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpHandshakeRes)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded HandshakeRes
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded.SessionID != res.SessionID {
			t.Errorf("SessionID mismatch: got %v, want %v", decoded.SessionID, res.SessionID)
		}
		if decoded.Status != res.Status {
			t.Errorf("Status mismatch: got %d, want %d", decoded.Status, res.Status)
		}
		if decoded.SpawnX != res.SpawnX || decoded.SpawnY != res.SpawnY || decoded.SpawnZ != res.SpawnZ {
			t.Errorf("Spawn coordinates mismatch: (%f,%f,%f)", decoded.SpawnX, decoded.SpawnY, decoded.SpawnZ)
		}
		if decoded.WorldTime != res.WorldTime {
			t.Errorf("WorldTime mismatch: got %d, want %d", decoded.WorldTime, res.WorldTime)
		}
		if decoded.EcoTier != res.EcoTier {
			t.Errorf("EcoTier mismatch: got %d, want %d", decoded.EcoTier, res.EcoTier)
		}
	})

	t.Run("OpHeartbeat Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		hb := HeartbeatPayload{Timestamp: 1716383000}
		const expectedLen = 8

		if err := WritePacket(&buf, OpHeartbeat, 12, FlagUnreliable, hb); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpHeartbeat {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpHeartbeat)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded HeartbeatPayload
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded.Timestamp != hb.Timestamp {
			t.Errorf("Timestamp mismatch: got %d, want %d", decoded.Timestamp, hb.Timestamp)
		}
	})

	t.Run("OpClientTransform Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		ct := ClientTransform{
			SessionID: [4]byte{0x10, 0x20, 0x30, 0x40},
			PosX:      123.456,
			PosY:      12.34,
			PosZ:      -987.654,
			Yaw:       1800,
			Pitch:     -450,
			VelX:      100,
			VelY:      0,
			VelZ:      -150,
			AnimFlags: 0x05,
		}

		const expectedLen = 4 + 4 + 4 + 4 + 2 + 2 + 2 + 2 + 2 + 1 // 27 bytes
		if err := WritePacket(&buf, OpClientTransform, 13, FlagUnreliable, ct); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpClientTransform {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpClientTransform)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded ClientTransform
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded.SessionID != ct.SessionID {
			t.Errorf("SessionID mismatch: got %v, want %v", decoded.SessionID, ct.SessionID)
		}
		if decoded.PosX != ct.PosX || decoded.PosY != ct.PosY || decoded.PosZ != ct.PosZ {
			t.Errorf("Position mismatch: (%f,%f,%f)", decoded.PosX, decoded.PosY, decoded.PosZ)
		}
		if decoded.Yaw != ct.Yaw || decoded.Pitch != ct.Pitch {
			t.Errorf("Angles mismatch: yaw=%d pitch=%d", decoded.Yaw, decoded.Pitch)
		}
		if decoded.VelX != ct.VelX || decoded.VelY != ct.VelY || decoded.VelZ != ct.VelZ {
			t.Errorf("Velocity mismatch: (%d,%d,%d)", decoded.VelX, decoded.VelY, decoded.VelZ)
		}
		if decoded.AnimFlags != ct.AnimFlags {
			t.Errorf("AnimFlags mismatch: got %d, want %d", decoded.AnimFlags, ct.AnimFlags)
		}
	})

	t.Run("OpServerSnapshot Dynamic Entries and Layout", func(t *testing.T) {
		snap := ServerSnapshot{
			Count: 3,
			Entries: [64]SnapshotEntry{
				{SessionID: 1, PosX: 10, PosY: 20, PosZ: 30, Yaw: 100, Pitch: 10, AnimFlags: 1, Health: 100},
				{SessionID: 2, PosX: -10, PosY: -20, PosZ: -30, Yaw: -100, Pitch: -10, AnimFlags: 2, Health: 85},
				{SessionID: 3, PosX: 0, PosY: 5, PosZ: 100, Yaw: 0, Pitch: 0, AnimFlags: 0, Health: 40},
			},
		}

		var buf bytes.Buffer
		if err := WriteServerSnapshot(&buf, 14, FlagUnreliable, &snap); err != nil {
			t.Fatalf("WriteServerSnapshot failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpServerSnapshot {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpServerSnapshot)
		}
		// 1 (Count) + 3 * 22 (SnapshotEntry) = 67 bytes
		const expectedLen = 1 + 3*22
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		decoded, err := ReadServerSnapshot(&buf)
		if err != nil {
			t.Fatalf("ReadServerSnapshot failed: %v", err)
		}
		if decoded.Count != 3 {
			t.Fatalf("Count mismatch: got %d, want 3", decoded.Count)
		}
		for i := 0; i < 3; i++ {
			if decoded.Entries[i] != snap.Entries[i] {
				t.Errorf("Entry %d mismatch: got %+v, want %+v", i, decoded.Entries[i], snap.Entries[i])
			}
		}
	})

	t.Run("OpServerSnapshot Value Form Serialization", func(t *testing.T) {
		snap := ServerSnapshot{
			Count: 1,
			Entries: [64]SnapshotEntry{
				{SessionID: 999, PosX: 1.0, PosY: 2.0, PosZ: 3.0, Yaw: 50, Pitch: -50, AnimFlags: 4, Health: 99},
			},
		}

		var buf bytes.Buffer
		if err := WritePacket(&buf, OpServerSnapshot, 15, FlagUnreliable, snap); err != nil {
			t.Fatalf("WritePacket value form failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		const expectedLen = 1 + 1*22
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		decoded, err := ReadServerSnapshot(&buf)
		if err != nil {
			t.Fatalf("ReadServerSnapshot failed: %v", err)
		}
		if decoded.Count != 1 || decoded.Entries[0] != snap.Entries[0] {
			t.Errorf("Decoded entry mismatch: %+v", decoded)
		}
	})

	t.Run("OpEntityEnterAoI and OpEntityLeaveAoI", func(t *testing.T) {
		enter := EntityEnterAoI{
			EntityID:   505,
			EntityType: 1, // Stalker
			PosX:       15.5,
			PosY:       0.0,
			PosZ:       -42.25,
			Health:     100,
		}
		copy(enter.Section[:], "sim_default_stalker_novice")
		copy(enter.Faction[:], "loner")

		const expectedEnterLen = 4 + 1 + 32 + 4 + 4 + 4 + 16 + 1 // 62 bytes
		var buf bytes.Buffer
		if err := WritePacket(&buf, OpEntityEnterAoI, 16, FlagReliable, enter); err != nil {
			t.Fatalf("WritePacket OpEntityEnterAoI failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpEntityEnterAoI {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpEntityEnterAoI)
		}
		if hdr.PayloadLength != expectedEnterLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedEnterLen)
		}

		var decodedEnter EntityEnterAoI
		if err := binary.Read(&buf, binary.LittleEndian, &decodedEnter); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decodedEnter.EntityID != enter.EntityID || decodedEnter.Health != enter.Health {
			t.Errorf("Enter fields mismatch: %+v", decodedEnter)
		}

		// Leave AoI
		leave := EntityLeaveAoI{EntityID: 505}
		const expectedLeaveLen = 4
		buf.Reset()
		if err := WritePacket(&buf, OpEntityLeaveAoI, 17, FlagReliable, leave); err != nil {
			t.Fatalf("WritePacket OpEntityLeaveAoI failed: %v", err)
		}

		hdr, err = ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpEntityLeaveAoI {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpEntityLeaveAoI)
		}
		if hdr.PayloadLength != expectedLeaveLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLeaveLen)
		}

		var decodedLeave EntityLeaveAoI
		if err := binary.Read(&buf, binary.LittleEndian, &decodedLeave); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decodedLeave.EntityID != leave.EntityID {
			t.Errorf("Leave EntityID mismatch: got %d, want %d", decodedLeave.EntityID, leave.EntityID)
		}
	})

	t.Run("OpLeave and OpEnter Payload Tests", func(t *testing.T) {
		enter := EnterAoIPayload{
			SessionID: 101,
			PosX:      55.5,
			PosY:      1.2,
			PosZ:      -88.8,
		}
		const expectedEnterLen = 4 + 4 + 4 + 4 // 16 bytes
		var buf bytes.Buffer
		if err := WritePacket(&buf, OpEntityEnterAoI, 18, FlagReliable, enter); err != nil {
			t.Fatalf("WritePacket OpEntityEnterAoI failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.PayloadLength != expectedEnterLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedEnterLen)
		}

		var decodedEnter EnterAoIPayload
		if err := binary.Read(&buf, binary.LittleEndian, &decodedEnter); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decodedEnter != enter {
			t.Errorf("EnterAoIPayload mismatch: got %+v, want %+v", decodedEnter, enter)
		}

		leave := LeaveAoIPayload{SessionID: 101}
		const expectedLeaveLen = 4
		buf.Reset()
		if err := WritePacket(&buf, OpEntityLeaveAoI, 19, FlagReliable, leave); err != nil {
			t.Fatalf("WritePacket OpEntityLeaveAoI failed: %v", err)
		}

		hdr, err = ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.PayloadLength != expectedLeaveLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLeaveLen)
		}

		var decodedLeave LeaveAoIPayload
		if err := binary.Read(&buf, binary.LittleEndian, &decodedLeave); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decodedLeave != leave {
			t.Errorf("LeaveAoIPayload mismatch: got %+v, want %+v", decodedLeave, leave)
		}
	})

	t.Run("OpSafezoneState Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		sz := SafezoneStatePayload{InSafeZone: 1}
		const expectedLen = 1

		if err := WritePacket(&buf, OpSafezoneState, 20, FlagReliable, sz); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpSafezoneState {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpSafezoneState)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded SafezoneStatePayload
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded.InSafeZone != 1 {
			t.Errorf("InSafeZone mismatch: got %d, want 1", decoded.InSafeZone)
		}
	})

	t.Run("OpDamageNotify Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		dmg := DamageNotify{
			TargetID:   1001,
			AttackerID: 2002,
			Damage:     45.5,
			BoneID:     15,
		}

		const expectedLen = 4 + 4 + 4 + 1 // 13 bytes
		if err := WritePacket(&buf, OpDamageNotify, 21, FlagReliable, dmg); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpDamageNotify {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpDamageNotify)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded DamageNotify
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded != dmg {
			t.Errorf("Decoded mismatch: got %+v, want %+v", decoded, dmg)
		}
	})

	t.Run("OpWorldEvent Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		ev := WorldEventPayload{
			EventType: 0, // Emission
			State:     2, // Active
			Timer:     120,
		}

		const expectedLen = 1 + 1 + 4 // 6 bytes
		if err := WritePacket(&buf, OpWorldEvent, 22, FlagReliable, ev); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpWorldEvent {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpWorldEvent)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded WorldEventPayload
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded != ev {
			t.Errorf("Decoded mismatch: got %+v, want %+v", decoded, ev)
		}
	})

	t.Run("OpChatText Roundtrip and Layout", func(t *testing.T) {
		msg := "Attention Stalkers: an emission is imminent!"
		ct := NewChatText(42, msg)
		const expectedLen = 4 + 1 + 255 // 260 bytes

		var buf bytes.Buffer
		if err := WritePacket(&buf, OpChatText, 23, FlagReliable, ct); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpChatText {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpChatText)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded ChatText
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded.SenderID != 42 {
			t.Errorf("SenderID mismatch: got %d, want 42", decoded.SenderID)
		}
		if int(decoded.Len) != len(msg) {
			t.Errorf("Len mismatch: got %d, want %d", decoded.Len, len(msg))
		}
		if string(decoded.Text[:decoded.Len]) != msg {
			t.Errorf("Text mismatch: got %s, want %s", string(decoded.Text[:decoded.Len]), msg)
		}

		// Test truncation at 255
		longMsg := strings.Repeat("A", 300)
		ctLong := NewChatText(1, longMsg)
		if ctLong.Len != 255 {
			t.Errorf("Expected truncated length 255, got %d", ctLong.Len)
		}
	})

	t.Run("OpAiAction Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		ai := AIActionPayload{
			SquadID: 301,
			State:   2, // In Combat
			PosX:    12.34,
			PosY:    -5.67,
			PosZ:    89.01,
		}

		const expectedLen = 4 + 1 + 4 + 4 + 4 // 17 bytes
		if err := WritePacket(&buf, OpAIActionEvent, 24, FlagUnreliable, ai); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpAIActionEvent {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpAIActionEvent)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded AIActionPayload
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded != ai {
			t.Errorf("Decoded mismatch: got %+v, want %+v", decoded, ai)
		}
	})

	t.Run("OpStashInteract and OpStashResponse Roundtrip and Layout", func(t *testing.T) {
		interact := StashInteractPayload{
			StashID: 10,
			Action:  2, // Take
			Count:   5,
		}
		copy(interact.ItemSection[:], "medkit_scientic")

		const expectedInteractLen = 4 + 1 + 32 + 2 // 39 bytes
		var buf bytes.Buffer
		if err := WritePacket(&buf, OpStashInteract, 25, FlagReliable, interact); err != nil {
			t.Fatalf("WritePacket OpStashInteract failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpStashInteract {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpStashInteract)
		}
		if hdr.PayloadLength != expectedInteractLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedInteractLen)
		}

		var decodedInteract StashInteractPayload
		if err := binary.Read(&buf, binary.LittleEndian, &decodedInteract); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decodedInteract != interact {
			t.Errorf("Decoded interact mismatch: got %+v, want %+v", decodedInteract, interact)
		}

		// Response
		response := StashResponsePayload{
			StashID: 10,
			Status:  0, // OK
			Count:   3,
		}
		copy(response.Data[:], `[{"sec":"medkit", "cnt":3}]`)

		const expectedResponseLen = 4 + 1 + 2 + 256 // 263 bytes
		buf.Reset()
		if err := WritePacket(&buf, OpStashResponse, 26, FlagReliable, response); err != nil {
			t.Fatalf("WritePacket OpStashResponse failed: %v", err)
		}

		hdr, err = ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.Opcode != OpStashResponse {
			t.Errorf("Opcode mismatch: got %d, want %d", hdr.Opcode, OpStashResponse)
		}
		if hdr.PayloadLength != expectedResponseLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedResponseLen)
		}

		var decodedResponse StashResponsePayload
		if err := binary.Read(&buf, binary.LittleEndian, &decodedResponse); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decodedResponse != response {
			t.Errorf("Decoded response mismatch: got %+v, want %+v", decodedResponse, response)
		}
	})

	t.Run("EntityAoIPayload Roundtrip and Layout", func(t *testing.T) {
		var buf bytes.Buffer
		aoi := EntityAoIPayload{
			EntityID:   777,
			EntityType: 0, // AI squad
			PosX:       100.5,
			PosY:       -10.2,
			PosZ:       200.75,
		}

		const expectedLen = 4 + 1 + 4 + 4 + 4 // 17 bytes
		if err := WritePacket(&buf, OpEntityEnterAoI, 27, FlagReliable, aoi); err != nil {
			t.Fatalf("WritePacket failed: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("ReadHeader failed: %v", err)
		}
		if hdr.PayloadLength != expectedLen {
			t.Fatalf("Payload length mismatch: got %d, want %d", hdr.PayloadLength, expectedLen)
		}

		var decoded EntityAoIPayload
		if err := binary.Read(&buf, binary.LittleEndian, &decoded); err != nil {
			t.Fatalf("Payload read failed: %v", err)
		}
		if decoded != aoi {
			t.Errorf("Decoded mismatch: got %+v, want %+v", decoded, aoi)
		}
	})

	t.Run("Header Incomplete Reads", func(t *testing.T) {
		// Less than 12 bytes header (Header is uint16+uint8+uint8+uint32+uint16+uint16 = 12 bytes)
		partial := []byte{0x5A, 0x4F, 0x01}
		r := bytes.NewReader(partial)
		_, err := ReadHeader(r)
		if err != io.ErrUnexpectedEOF {
			t.Errorf("Expected io.ErrUnexpectedEOF, got %v", err)
		}
	})
}
