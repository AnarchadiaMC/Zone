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
}
