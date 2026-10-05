package protocol

import (
	"bytes"
	"testing"
)

func benchSnapshot32() *ServerSnapshot {
	snap := &ServerSnapshot{Count: 32}
	for i := 0; i < len(snap.Entries); i++ {
		snap.Entries[i] = SnapshotEntry{
			SessionID: uint32(i + 1),
			PosX:      float32(i) * 1.5,
			PosY:      float32(i) * 0.25,
			PosZ:      float32(i) * -2.5,
			Yaw:       int16(i * 100),
			Pitch:     int16(-i * 50),
			AnimFlags: uint8(i),
			Health:    uint8(100 - i),
		}
	}
	return snap
}

func BenchmarkWritePacket_ServerSnapshot32(b *testing.B) {
	snap := benchSnapshot32()
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := WritePacket(&buf, OpServerSnapshot, uint32(i), FlagUnreliable, snap); err != nil {
			b.Fatalf("WritePacket: %v", err)
		}
	}
}

func BenchmarkWritePacket_ServerSnapshot16(b *testing.B) {
	snap := benchSnapshot32()
	snap.Count = 16
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := WritePacket(&buf, OpServerSnapshot, uint32(i), FlagUnreliable, snap); err != nil {
			b.Fatalf("WritePacket: %v", err)
		}
	}
}

func BenchmarkReadServerSnapshot32(b *testing.B) {
	snap := benchSnapshot32()
	var raw bytes.Buffer
	if err := WritePacket(&raw, OpServerSnapshot, 1, FlagUnreliable, snap); err != nil {
		b.Fatalf("WritePacket: %v", err)
	}
	hdr, err := ReadHeader(bytes.NewReader(raw.Bytes()))
	if err != nil {
		b.Fatalf("ReadHeader: %v", err)
	}
	payload := raw.Bytes()[12 : 12+int(hdr.PayloadLength)]
	var reader bytes.Reader
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader.Reset(payload)
		if _, err := ReadServerSnapshot(&reader); err != nil {
			b.Fatalf("ReadServerSnapshot: %v", err)
		}
	}
}
