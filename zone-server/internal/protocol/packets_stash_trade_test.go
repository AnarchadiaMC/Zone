package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The stash/trade/wallet opcodes are frozen for protocol v6.
func TestStashTradeOpcodesFrozen(t *testing.T) {
	cases := []struct {
		name string
		got  uint16
		want uint16
	}{
		{"OpStashAction", OpStashAction, 0x0081},
		{"OpStashResult", OpStashResult, 0x0082},
		{"OpTradeAction", OpTradeAction, 0x0083},
		{"OpTradeResult", OpTradeResult, 0x0084},
		{"OpWalletUpdate", OpWalletUpdate, 0x0085},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = 0x%04X, want 0x%04X", tc.name, tc.got, tc.want)
		}
	}
}

// binary.Size must report the frozen widths: 84-byte stash action, 73-byte
// stash result, 76-byte trade action, 75-byte trade result and the 4-byte
// wallet update.
func TestStashTradeByteLengths(t *testing.T) {
	cases := []struct {
		name string
		val  interface{}
		want int
	}{
		{"StashActionPacket", StashActionPacket{}, 84},
		{"StashResultPacket", StashResultPacket{}, 73},
		{"TradeActionPacket", TradeActionPacket{}, 76},
		{"TradeResultPacket", TradeResultPacket{}, 75},
		{"WalletUpdatePacket", WalletUpdatePacket{}, 4},
	}
	for _, tc := range cases {
		if got := binary.Size(tc.val); got != tc.want {
			t.Errorf("%s binary.Size = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestStashActionRoundTrip(t *testing.T) {
	in := StashActionPacket{
		ActionID:  88001,
		Action:    StashActionStore,
		X:         12.25,
		Y:         -3.75,
		Z:         800.5,
		Count:     7,
		Condition: 80,
	}
	copy(in.Section[:], "bandage")

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpStashAction, 201, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpStashAction || hdr.FlagsChannel != FlagReliable || hdr.PayloadLength != 84 {
		t.Fatalf("header = %+v, want op=0x%04X flags=reliable len=84", hdr, OpStashAction)
	}

	var out StashActionPacket
	if err := binary.Read(r, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
}

func TestStashResultRoundTripSignedDelta(t *testing.T) {
	in := StashResultPacket{
		ActionID:  88001,
		Result:    ItemResultOK,
		Action:    StashActionTake,
		Count:     -5,
		Condition: 55,
	}
	copy(in.Section[:], "medkit")

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpStashResult, 202, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpStashResult || hdr.FlagsChannel != FlagReliable || hdr.PayloadLength != 73 {
		t.Fatalf("header = %+v, want op=0x%04X flags=reliable len=73", hdr, OpStashResult)
	}

	var out StashResultPacket
	if err := binary.Read(r, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
	if out.Count != -5 {
		t.Fatalf("signed count not preserved: got %d, want -5", out.Count)
	}
}

func TestTradeActionRoundTrip(t *testing.T) {
	in := TradeActionPacket{
		ActionID:   99001,
		Action:     TradeActionSell,
		MoneyDelta: 2500,
		Count:      3,
		Condition:  40,
	}
	copy(in.Section[:], "wpn_pm")

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpTradeAction, 203, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpTradeAction || hdr.FlagsChannel != FlagReliable || hdr.PayloadLength != 76 {
		t.Fatalf("header = %+v, want op=0x%04X flags=reliable len=76", hdr, OpTradeAction)
	}

	var out TradeActionPacket
	if err := binary.Read(r, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
}

func TestTradeResultRoundTripSignedMoney(t *testing.T) {
	in := TradeResultPacket{
		ActionID:   99002,
		Result:     ItemResultCorrected,
		Action:     TradeActionBuy,
		MoneyDelta: -200000,
		Condition:  100,
	}
	copy(in.Section[:], "medkit")

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpTradeResult, 204, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpTradeResult || hdr.FlagsChannel != FlagReliable || hdr.PayloadLength != 75 {
		t.Fatalf("header = %+v, want op=0x%04X flags=reliable len=75", hdr, OpTradeResult)
	}

	var out TradeResultPacket
	if err := binary.Read(r, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
	if out.MoneyDelta != -200000 {
		t.Fatalf("signed money not preserved: got %d, want -200000", out.MoneyDelta)
	}
}

func TestWalletUpdateRoundTrip(t *testing.T) {
	in := WalletUpdatePacket{Money: 123456}

	var buf bytes.Buffer
	if err := WritePacket(&buf, OpWalletUpdate, 205, FlagReliable, in); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	r := bytes.NewReader(buf.Bytes())
	hdr, err := ReadHeader(r)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if hdr.Opcode != OpWalletUpdate || hdr.FlagsChannel != FlagReliable || hdr.PayloadLength != 4 {
		t.Fatalf("header = %+v, want op=0x%04X flags=reliable len=4", hdr, OpWalletUpdate)
	}

	var out WalletUpdatePacket
	if err := binary.Read(r, binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", out, in)
	}
}

// Handler decode path: one byte short must fail, exact size must decode.
func TestStashTradeMalformedShortPayloadsRejected(t *testing.T) {
	if err := decodeShort(t, &StashActionPacket{}, 83); err == nil {
		t.Error("83-byte StashActionPacket payload decoded without error")
	}
	if err := decodeShort(t, &StashResultPacket{}, 72); err == nil {
		t.Error("72-byte StashResultPacket payload decoded without error")
	}
	if err := decodeShort(t, &TradeActionPacket{}, 75); err == nil {
		t.Error("75-byte TradeActionPacket payload decoded without error")
	}
	if err := decodeShort(t, &TradeResultPacket{}, 74); err == nil {
		t.Error("74-byte TradeResultPacket payload decoded without error")
	}
	if err := decodeShort(t, &WalletUpdatePacket{}, 3); err == nil {
		t.Error("3-byte WalletUpdatePacket payload decoded without error")
	}

	if err := decodeShort(t, &StashActionPacket{}, 84); err != nil {
		t.Errorf("84-byte StashActionPacket rejected: %v", err)
	}
	if err := decodeShort(t, &StashResultPacket{}, 73); err != nil {
		t.Errorf("73-byte StashResultPacket rejected: %v", err)
	}
	if err := decodeShort(t, &TradeActionPacket{}, 76); err != nil {
		t.Errorf("76-byte TradeActionPacket rejected: %v", err)
	}
	if err := decodeShort(t, &TradeResultPacket{}, 75); err != nil {
		t.Errorf("75-byte TradeResultPacket rejected: %v", err)
	}
	if err := decodeShort(t, &WalletUpdatePacket{}, 4); err != nil {
		t.Errorf("4-byte WalletUpdatePacket rejected: %v", err)
	}
}
