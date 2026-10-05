package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestNetcodeOpcodesFrozen(t *testing.T) {
	cases := []struct {
		name string
		got  uint16
		want uint16
	}{
		{"OpPositionCorrection", OpPositionCorrection, 0x007B},
		{"OpItemAction", OpItemAction, 0x007D},
		{"OpItemUpdate", OpItemUpdate, 0x007E},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = 0x%04X, want 0x%04X", tc.name, tc.got, tc.want)
		}
	}
}

func TestNetcodeByteLengths(t *testing.T) {
	cases := []struct {
		name string
		val  interface{}
		want int
	}{
		{"PositionCorrection", PositionCorrection{}, 12},
		{"ItemActionPacket", ItemActionPacket{}, 88},
		{"ItemUpdatePacket", ItemUpdatePacket{}, 89},
	}
	for _, tc := range cases {
		if got := binary.Size(tc.val); got != tc.want {
			t.Errorf("%s binary.Size = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestPositionCorrectionRoundTrip(t *testing.T) {
	in := PositionCorrection{X: -211.3, Y: -20.2, Z: -145.8}

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpPositionCorrection, 77, FlagUnreliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	hdr, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpPositionCorrection {
		t.Fatalf("opcode = 0x%04X, want 0x%04X", hdr.Opcode, OpPositionCorrection)
	}
	if hdr.PayloadLength != 12 {
		t.Fatalf("payload length = %d, want 12", hdr.PayloadLength)
	}
	if hdr.FlagsChannel != FlagUnreliable {
		t.Fatalf("flags = 0x%02X, want FlagUnreliable", hdr.FlagsChannel)
	}

	var out PositionCorrection
	if err := binary.Read(&buf, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
}

func TestItemActionRoundTrip(t *testing.T) {
	in := ItemActionPacket{
		ActionID:  123456,
		Action:    ItemActionDrop,
		ItemID:    0,
		Count:     7,
		X:         12.5,
		Y:         -3.25,
		Z:         800.125,
		Condition: 64,
	}
	copy(in.Section[:], "bandage")

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpItemAction, 78, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	hdr, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpItemAction {
		t.Fatalf("opcode = 0x%04X, want 0x%04X", hdr.Opcode, OpItemAction)
	}
	if hdr.PayloadLength != 88 {
		t.Fatalf("payload length = %d, want 88", hdr.PayloadLength)
	}
	if hdr.FlagsChannel != FlagReliable {
		t.Fatalf("flags = 0x%02X, want FlagReliable", hdr.FlagsChannel)
	}

	var out ItemActionPacket
	if err := binary.Read(&buf, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
}

func TestItemUpdateRoundTripSignedDelta(t *testing.T) {
	in := ItemUpdatePacket{
		ActionID:  654321,
		Result:    ItemResultOK,
		Action:    ItemActionPickup,
		ItemID:    42,
		Count:     -3,
		X:         1.5,
		Y:         2.5,
		Z:         3.5,
		Condition: 90,
	}
	copy(in.Section[:], "medkit")

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpItemUpdate, 79, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	hdr, err := ReadHeader(&buf)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpItemUpdate {
		t.Fatalf("opcode = 0x%04X, want 0x%04X", hdr.Opcode, OpItemUpdate)
	}
	if hdr.PayloadLength != 89 {
		t.Fatalf("payload length = %d, want 89", hdr.PayloadLength)
	}
	if hdr.FlagsChannel != FlagReliable {
		t.Fatalf("flags = 0x%02X, want FlagReliable", hdr.FlagsChannel)
	}

	var out ItemUpdatePacket
	if err := binary.Read(&buf, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
	if out.Count != -3 {
		t.Fatalf("signed count not preserved: got %d, want -3", out.Count)
	}
}

// decodeShort is the decoder path the UDP handlers use: feed a payload that is
// one byte shorter than the frozen size and require an error.
func decodeShort(t *testing.T, val interface{}, have int) error {
	t.Helper()
	raw := make([]byte, have)
	return binary.Read(bytes.NewReader(raw), binary.LittleEndian, val)
}

func TestNetcodeMalformedShortPayloadsRejected(t *testing.T) {
	if err := decodeShort(t, &PositionCorrection{}, 11); err == nil {
		t.Error("11-byte PositionCorrection payload decoded without error")
	}
	if err := decodeShort(t, &ItemActionPacket{}, 87); err == nil {
		t.Error("87-byte ItemActionPacket payload decoded without error")
	}
	if err := decodeShort(t, &ItemUpdatePacket{}, 88); err == nil {
		t.Error("88-byte ItemUpdatePacket payload decoded without error")
	}
	if err := decodeShort(t, &ItemActionPacket{}, 0); err == nil {
		t.Error("empty ItemActionPacket payload decoded without error")
	}

	// The correct sizes still decode cleanly.
	if err := decodeShort(t, &PositionCorrection{}, 12); err != nil {
		t.Errorf("12-byte PositionCorrection rejected: %v", err)
	}
	if err := decodeShort(t, &ItemActionPacket{}, 88); err != nil {
		t.Errorf("88-byte ItemActionPacket rejected: %v", err)
	}
	if err := decodeShort(t, &ItemUpdatePacket{}, 89); err != nil {
		t.Errorf("89-byte ItemUpdatePacket rejected: %v", err)
	}
}
