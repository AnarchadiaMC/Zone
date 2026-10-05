package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The AI replication and container opcodes are frozen for protocol v5.
func TestAIContainerOpcodesFrozen(t *testing.T) {
	cases := []struct {
		name string
		got  uint16
		want uint16
	}{
		{"OpAIState", OpAIState, 0x007C},
		{"OpContainerAction", OpContainerAction, 0x007F},
		{"OpContainerUpdate", OpContainerUpdate, 0x0080},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = 0x%04X, want 0x%04X", tc.name, tc.got, tc.want)
		}
	}
}

// binary.Size must report the frozen struct widths: 19-byte AI entry, the
// 1 + 32*19 maximum OpAIState payload, the 88-byte container request and the
// 77-byte container update.
func TestAIContainerByteLengths(t *testing.T) {
	cases := []struct {
		name string
		val  interface{}
		want int
	}{
		{"AIStateEntry", AIStateEntry{}, AIStateEntryWireSize},
		{"AIStatePacket", AIStatePacket{}, 1 + MaxAIStateEntries*AIStateEntryWireSize},
		{"ContainerActionPacket", ContainerActionPacket{}, 88},
		{"ContainerUpdatePacket", ContainerUpdatePacket{}, 77},
	}
	for _, tc := range cases {
		if got := binary.Size(tc.val); got != tc.want {
			t.Errorf("%s binary.Size = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestAIStateRoundTripSparse(t *testing.T) {
	in := AIStatePacket{Count: 2}
	in.Entries[0] = AIStateEntry{
		EntityID: 1_000_001,
		X:        -211.5,
		Y:        -20.25,
		Z:        -145.75,
		Yaw:      49152,
		Anim:     AIAnimWalk,
	}
	in.Entries[1] = AIStateEntry{
		EntityID: 1_000_002,
		X:        12.5,
		Y:        0,
		Z:        -3.25,
		Yaw:      0,
		Anim:     AIAnimIdle,
	}

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpAIState, 101, FlagUnreliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	// Sparse encoding: only Count entries are on the wire even though the
	// struct array holds MaxAIStateEntries slots.
	expectedPayload := 1 + 2*AIStateEntryWireSize
	if buf.Len() != PacketHeaderWireSize+expectedPayload {
		t.Fatalf("packet length = %d, want %d", buf.Len(), PacketHeaderWireSize+expectedPayload)
	}

	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpAIState || hdr.FlagsChannel != FlagUnreliable || hdr.PayloadLength != uint16(expectedPayload) {
		t.Fatalf("header = %+v, want op=0x%04X flags=unreliable len=%d", hdr, OpAIState, expectedPayload)
	}

	out, err := ReadAIStatePacket(r)
	if err != nil {
		t.Fatalf("ReadAIStatePacket: %v", err)
	}
	if out.Count != in.Count {
		t.Fatalf("count = %d, want %d", out.Count, in.Count)
	}
	for i := 0; i < int(in.Count); i++ {
		if out.Entries[i] != in.Entries[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, out.Entries[i], in.Entries[i])
		}
	}
}

// A full 32-entry chunk must fit the 1200-byte safe MTU: 12-byte header plus
// 1 + 32*19 = 609 payload bytes.
func TestAIStateMaxChunkFitsMTU(t *testing.T) {
	var in AIStatePacket
	in.Count = MaxAIStateEntries
	for i := range in.Entries {
		in.Entries[i] = AIStateEntry{EntityID: uint32(1_000_000 + i), X: float32(i), Anim: AIAnimRun}
	}

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpAIState, 1, FlagUnreliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	if buf.Len() > MaxSafeUDPPacketSize {
		t.Fatalf("full AI chunk = %d bytes, exceeds MTU %d", buf.Len(), MaxSafeUDPPacketSize)
	}
	if got, want := buf.Len(), PacketHeaderWireSize+1+MaxAIStateEntries*AIStateEntryWireSize; got != want {
		t.Fatalf("full AI chunk = %d bytes, want %d", got, want)
	}
}

func TestContainerActionRoundTrip(t *testing.T) {
	in := ContainerActionPacket{
		ActionID:    777001,
		ContainerID: 42,
		Action:      ContainerActionDeposit,
		Count:       7,
		X:           12.5,
		Y:           -3.25,
		Z:           800.125,
		Condition:   80,
	}
	copy(in.Section[:], "bandage")

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpContainerAction, 102, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpContainerAction || hdr.FlagsChannel != FlagReliable || hdr.PayloadLength != 88 {
		t.Fatalf("header = %+v, want op=0x%04X flags=reliable len=88", hdr, OpContainerAction)
	}

	var out ContainerActionPacket
	if err := binary.Read(r, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
}

func TestContainerUpdateRoundTripSignedDelta(t *testing.T) {
	in := ContainerUpdatePacket{
		ActionID:    777001,
		Result:      ItemResultOK,
		Action:      ContainerActionWithdraw,
		ContainerID: 9,
		Count:       -3,
		Condition:   55,
	}
	copy(in.Section[:], "medkit")

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpContainerUpdate, 103, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpContainerUpdate || hdr.FlagsChannel != FlagReliable || hdr.PayloadLength != 77 {
		t.Fatalf("header = %+v, want op=0x%04X flags=reliable len=77", hdr, OpContainerUpdate)
	}

	var out ContainerUpdatePacket
	if err := binary.Read(r, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
	if out.Count != -3 {
		t.Fatalf("signed count not preserved: got %d, want -3", out.Count)
	}
}

func TestContainerMalformedShortPayloadsRejected(t *testing.T) {
	if err := decodeShort(t, &ContainerActionPacket{}, 87); err == nil {
		t.Error("87-byte ContainerActionPacket payload decoded without error")
	}
	if err := decodeShort(t, &ContainerUpdatePacket{}, 76); err == nil {
		t.Error("76-byte ContainerUpdatePacket payload decoded without error")
	}
	if err := decodeShort(t, &ContainerActionPacket{}, 88); err != nil {
		t.Errorf("88-byte ContainerActionPacket rejected: %v", err)
	}
	if err := decodeShort(t, &ContainerUpdatePacket{}, 77); err != nil {
		t.Errorf("77-byte ContainerUpdatePacket rejected: %v", err)
	}
}
