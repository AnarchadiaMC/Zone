package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestWritePacket_InventorySyncLayout freezes the OpInventorySync (0x0076)
// wire layout: itemCount u8, then per item section[64], count u16,
// condition u8, ammo u16, slot i8.
func TestWritePacket_InventorySyncLayout(t *testing.T) {
	var payload InventorySyncPayload
	payload.ItemCount = 2
	copy(payload.Items[0].Section[:], "wpn_ak74")
	payload.Items[0].Count = 1
	payload.Items[0].Condition = 87
	payload.Items[0].Ammo = 30
	payload.Items[0].Slot = 1
	copy(payload.Items[1].Section[:], "medkit")
	payload.Items[1].Count = 3
	payload.Items[1].Condition = 100
	payload.Items[1].Ammo = 0
	payload.Items[1].Slot = -1

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpInventorySync, 7, FlagReliable, payload); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	const entrySize = 64 + 2 + 1 + 2 + 1
	expectedLen := 12 + 1 + 2*entrySize
	if buf.Len() != expectedLen {
		t.Fatalf("expected %d byte packet, got %d", expectedLen, buf.Len())
	}

	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpInventorySync {
		t.Fatalf("expected opcode 0x%04X, got 0x%04X", OpInventorySync, hdr.Opcode)
	}
	if int(hdr.PayloadLength) != 1+2*entrySize {
		t.Fatalf("expected payload length %d, got %d", 1+2*entrySize, hdr.PayloadLength)
	}

	var count uint8
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		t.Fatalf("read count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}

	var first InventoryItemPayload
	if err := binary.Read(r, binary.LittleEndian, &first); err != nil {
		t.Fatalf("read first entry: %v", err)
	}
	if sec := string(bytes.TrimRight(first.Section[:], "\x00")); sec != "wpn_ak74" {
		t.Errorf("expected section wpn_ak74, got %q", sec)
	}
	if first.Count != 1 || first.Condition != 87 || first.Ammo != 30 || first.Slot != 1 {
		t.Errorf("unexpected first entry: %+v", first)
	}

	var second InventoryItemPayload
	if err := binary.Read(r, binary.LittleEndian, &second); err != nil {
		t.Fatalf("read second entry: %v", err)
	}
	if sec := string(bytes.TrimRight(second.Section[:], "\x00")); sec != "medkit" {
		t.Errorf("expected section medkit, got %q", sec)
	}
	if second.Count != 3 || second.Slot != -1 {
		t.Errorf("unexpected second entry: %+v", second)
	}
}
