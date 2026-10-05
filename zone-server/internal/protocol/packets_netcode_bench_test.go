package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func BenchmarkNetcode_PositionCorrectionRoundTrip(b *testing.B) {
	in := PositionCorrection{X: 1.5, Y: -2.5, Z: 300.25}
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := WritePacket(&buf, OpPositionCorrection, uint32(i), FlagUnreliable, in); err != nil {
			b.Fatalf("WritePacket: %v", err)
		}
		var out PositionCorrection
		if err := binary.Read(bytes.NewReader(buf.Bytes()[12:]), binary.LittleEndian, &out); err != nil {
			b.Fatalf("decode: %v", err)
		}
	}
}

func BenchmarkNetcode_ItemActionRoundTrip(b *testing.B) {
	in := ItemActionPacket{
		ActionID:  1,
		Action:    ItemActionDrop,
		Count:     3,
		X:         10,
		Y:         1,
		Z:         20,
		Condition: 80,
	}
	copy(in.Section[:], "bandage")
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := WritePacket(&buf, OpItemAction, uint32(i), FlagReliable, in); err != nil {
			b.Fatalf("WritePacket: %v", err)
		}
		var out ItemActionPacket
		if err := binary.Read(bytes.NewReader(buf.Bytes()[12:]), binary.LittleEndian, &out); err != nil {
			b.Fatalf("decode: %v", err)
		}
	}
}

func BenchmarkNetcode_ItemUpdateRoundTrip(b *testing.B) {
	in := ItemUpdatePacket{
		ActionID:  1,
		Result:    ItemResultOK,
		Action:    ItemActionPickup,
		ItemID:    9,
		Count:     -1,
		X:         10,
		Y:         1,
		Z:         20,
		Condition: 80,
	}
	copy(in.Section[:], "bandage")
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := WritePacket(&buf, OpItemUpdate, uint32(i), FlagReliable, in); err != nil {
			b.Fatalf("WritePacket: %v", err)
		}
		var out ItemUpdatePacket
		if err := binary.Read(bytes.NewReader(buf.Bytes()[12:]), binary.LittleEndian, &out); err != nil {
			b.Fatalf("decode: %v", err)
		}
	}
}
