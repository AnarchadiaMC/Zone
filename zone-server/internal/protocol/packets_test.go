package protocol

import (
	"bytes"
	"testing"
)

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

	t.Run("Write and Read HeartbeatPayload", func(t *testing.T) {
		var buf bytes.Buffer
		hb := HeartbeatPayload{Timestamp: 1234567890}
		err := WritePacket(&buf, OpHeartbeat, 3, FlagUnreliable, hb)
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
		if hdr.PayloadLength != 8 {
			t.Errorf("Expected payload length 8, got %d", hdr.PayloadLength)
		}
	})

	t.Run("Write and Read ChatText", func(t *testing.T) {
		var buf bytes.Buffer
		ct := NewChatText(42, "Stalker, what brings you here?")
		err := WritePacket(&buf, OpChatText, 4, FlagReliable, ct)
		if err != nil {
			t.Fatalf("Failed to write packet: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("Failed to read header: %v", err)
		}
		if hdr.Opcode != OpChatText {
			t.Errorf("Expected Opcode %d, got %d", OpChatText, hdr.Opcode)
		}
		if ct.Len != uint8(len("Stalker, what brings you here?")) {
			t.Errorf("Unexpected ChatText length: %d", ct.Len)
		}
	})

	t.Run("SnapshotEntry Size and Serialization", func(t *testing.T) {
		var buf bytes.Buffer
		entry := SnapshotEntry{
			SessionID: 1001,
			PosX:      10.5,
			PosY:      20.25,
			PosZ:      30.125,
			Yaw:       1800,
			Pitch:     -450,
			AnimFlags: 3,
			Health:    100,
		}

		err := WritePacket(&buf, OpServerSnapshot, 5, FlagUnreliable, entry)
		if err != nil {
			t.Fatalf("Failed to write SnapshotEntry: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("Failed to read header: %v", err)
		}

		// 4 (SessionID) + 4 (PosX) + 4 (PosY) + 4 (PosZ) + 2 (Yaw) + 2 (Pitch) + 1 (AnimFlags) + 1 (Health) = 22 bytes
		const expectedPayloadLen = 22
		if hdr.PayloadLength != expectedPayloadLen {
			t.Errorf("Expected SnapshotEntry payload length %d, got %d (struct alignment corruption)", expectedPayloadLen, hdr.PayloadLength)
		}
	})

	t.Run("DamageNotify Size and Serialization", func(t *testing.T) {
		var buf bytes.Buffer
		dmg := DamageNotify{
			TargetID:   1001,
			AttackerID: 2002,
			Damage:     45.5,
			BoneID:     15,
		}

		err := WritePacket(&buf, OpDamageNotify, 6, FlagReliable, dmg)
		if err != nil {
			t.Fatalf("Failed to write DamageNotify: %v", err)
		}

		hdr, err := ReadHeader(&buf)
		if err != nil {
			t.Fatalf("Failed to read header: %v", err)
		}

		// 4 (TargetID) + 4 (AttackerID) + 4 (Damage) + 1 (BoneID) = 13 bytes
		const expectedPayloadLen = 13
		if hdr.PayloadLength != expectedPayloadLen {
			t.Errorf("Expected DamageNotify payload length %d, got %d", expectedPayloadLen, hdr.PayloadLength)
		}
	})
}

