package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

const (
	MaxSafeUDPPacketSize        = 1200
	OpAck                uint16 = 0x0005
)

// MaxSnapshotEntries is the wire capacity of one OpServerSnapshot packet.
// A client with more AoI neighbours than this must receive several snapshot
// packets per tick; the AoI broadcaster chunks nearest-first.
const MaxSnapshotEntries = 32

// SnapshotEntryWireSize is the fixed on-wire size of one SnapshotEntry.
const SnapshotEntryWireSize = 22

// PacketHeaderWireSize is the fixed on-wire size of PacketHeader.
const PacketHeaderWireSize = 12

var (
	ErrPayloadTooLarge   = errors.New("payload exceeds uint16 maximum")
	ErrPayloadExceedsMTU = errors.New("packet exceeds safe MTU threshold of 1200 bytes")
)

type HandshakeReq struct {
	UUID        [37]byte
	HWIDHash    [32]byte // 32-byte SHA-256 binary hash
	Nickname    [32]byte
	ProtocolVer uint8
}

type HandshakeRes struct {
	SessionID    [4]byte
	Status       uint8
	SpawnX       float32
	SpawnY       float32
	SpawnZ       float32
	WorldTime    uint64
	EcoTier      uint8
	HasCharacter uint8
	Faction      [16]byte
}

type ShowStart struct {
	Level   uint8
	PosX    float32
	PosY    float32
	PosZ    float32
	Flags   uint8
	EcoTier uint8
}

type CharacterSelect struct {
	Faction [16]byte
	Money   uint32
	Items   [256]byte
}

// LevelLoad is the OpLoadLevel (0x0073) payload: server-authoritative spawn
// target sent after handshake (returning player) or after CharacterSelect.
type LevelLoad struct {
	Level   uint8
	PosX    float32
	PosY    float32
	PosZ    float32
	Faction [16]byte
	EcoTier uint8
}

// InventoryItemPayload is one entry of OpInventorySync (0x0076). Count is the
// number of identical copies to create; condition is 0-100 (percent); ammo is
// loaded rounds for weapons or box content for ammo items; slot is -1 when
// unslotted.
type InventoryItemPayload struct {
	Section   [64]byte
	Count     uint16
	Condition uint8
	Ammo      uint16
	Slot      int8
}

// InventorySyncPayload is one chunk of a starter/restored inventory. A single
// packet carries at most MaxInventorySyncItems entries to stay under the 1200
// byte MTU; a full inventory arrives as several packets in order.
type InventorySyncPayload struct {
	ItemCount uint8
	Items     [MaxInventorySyncItems]InventoryItemPayload
}

// MaxInventorySyncItems is the per-packet entry cap: 1 + 16*70 = 1121 bytes,
// 1133 with the header, under MaxSafeUDPPacketSize.
const MaxInventorySyncItems = 16

// ErrorPayload is the OpError (0x0077) payload. Code values:
// 1 = invalid faction, 2 = invalid loadout, 3 = character creation failed.
type ErrorPayload struct {
	Code    uint8
	Message [96]byte
}

type ClientTransform struct {
	SessionID [4]byte
	PosX      float32
	PosY      float32
	PosZ      float32
	Yaw       int16
	Pitch     int16
	VelX      int16
	VelY      int16
	VelZ      int16
	AnimFlags uint8
	Gvid      uint16
}

type SnapshotEntry struct {
	SessionID uint32
	PosX      float32
	PosY      float32
	PosZ      float32
	Yaw       int16
	Pitch     int16
	AnimFlags uint8
	Health    uint8
}

type ServerSnapshot struct {
	Count   uint8
	Entries [MaxSnapshotEntries]SnapshotEntry
}

type SafezoneStatePayload struct {
	Locked uint8
	ZoneID [32]byte
}

// EntityEnterAoI is the OpEntityEnterAoI (0x0012) payload, frozen v3: 132
// bytes. Field offsets: entityID u32(0), type u8(4), section[64](5),
// posX f32(69), posY f32(73), posZ f32(77), faction[16](81), health u8(97),
// gvid u16(98), name[32](100..131).
type EntityEnterAoI struct {
	EntityID   uint32
	EntityType uint8
	Section    [64]byte
	PosX       float32
	PosY       float32
	PosZ       float32
	Faction    [16]byte
	Health     uint8
	Gvid       uint16
	Name       [32]byte
}

type EntityLeaveAoI struct {
	EntityID uint32
}

// ServerQueryRes is the OpServerQueryRes (0x0007) reply. Frozen v2 payload is
// 70 bytes: two 32-byte fixed strings and six single-byte fields.
type ServerQueryRes struct {
	Name       [32]byte
	Map        [32]byte
	Players    uint8
	MaxPlayers uint8
	Mode       uint8
	Locked     uint8
	ProtoVer   uint8
	TickRateHz uint8
}

// ReadServerQueryRes decodes a ServerQueryRes payload.
func ReadServerQueryRes(r io.Reader) (*ServerQueryRes, error) {
	buf := make([]byte, 70)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	if n < 70 {
		return nil, io.ErrUnexpectedEOF
	}
	var q ServerQueryRes
	copy(q.Name[:], buf[0:32])
	copy(q.Map[:], buf[32:64])
	q.Players = buf[64]
	q.MaxPlayers = buf[65]
	q.Mode = buf[66]
	q.Locked = buf[67]
	q.ProtoVer = buf[68]
	q.TickRateHz = buf[69]
	return &q, nil
}

// LevelChangePayload carries the destination level for OpLevelChange (0x0074).
type LevelChangePayload struct {
	Level [32]byte
}

// PlayerVisualPayload carries the actor section for OpPlayerVisual (0x0075).
type PlayerVisualPayload struct {
	Visual [64]byte
}

type DamageNotify struct {
	TargetID   uint32
	AttackerID uint32
	Damage     float32
	BoneID     uint8
}

type HeartbeatPayload struct {
	Timestamp uint64
}

// WorldEventPayload is sent via OpWorldEvent (0x0030) for Zone-wide events.
// EventType: 0 = Emission (future: 1 = Psi-Storm, etc.)
// State: maps to EmissionState (0=Dormant, 1=Warning, 2=Active, 3=Clear)
// Timer: seconds remaining until the next state transition
type WorldEventPayload struct {
	EventType uint8
	State     uint8
	Timer     uint32
}

type ChatText struct {
	SenderID uint32
	Len      uint8
	Text     [255]byte
}

func NewChatText(senderID uint32, msg string) ChatText {
	var ct ChatText
	ct.SenderID = senderID
	if len(msg) > 255 {
		msg = msg[:255]
	}
	ct.Len = uint8(len(msg))
	copy(ct.Text[:], msg)
	return ct
}

// GroupInviteNotify is the OpGroupInviteNotify (0x0078) server->client
// payload: 52 bytes. Sent to the invite target after a successful /invite.
type GroupInviteNotify struct {
	InviterSessionID uint32
	InviterName      [32]byte
	InviterFaction   [16]byte
}

// GroupResponse is the OpGroupResponse (0x0079) client->server payload.
// Accept is 1 to accept the pending invitation, 0 to decline it.
type GroupResponse struct {
	Accept uint8
}

// MaxGroupMembers is the wire capacity of OpGroupState (0x007A).
const MaxGroupMembers = 8

// GroupStateMember is one member entry of OpGroupState: 53 bytes.
type GroupStateMember struct {
	SessionID uint32
	Name      [32]byte
	Faction   [16]byte
	IsLeader  uint8
}

// GroupState is the OpGroupState (0x007A) server->client payload. The wire
// form carries MemberCount followed by exactly MemberCount member entries
// (1 + 53 each); an empty state is a single 0x00 byte.
type GroupState struct {
	MemberCount uint8
	Members     [MaxGroupMembers]GroupStateMember
}

// EntityAoIPayload notifies a client that an entity entered or left its
// Area of Interest.
type EntityAoIPayload struct {
	EntityID   uint32
	EntityType uint8 // 0 = AI squad, 1 = player
	PosX       float32
	PosY       float32
	PosZ       float32
}

// Item action identifiers for OpItemAction/OpItemUpdate.
const (
	ItemActionDrop    uint8 = 1
	ItemActionPickup  uint8 = 2
	ItemActionConsume uint8 = 3
)

// Container action identifiers for OpContainerAction.
const (
	ContainerActionDeposit  uint8 = 1
	ContainerActionWithdraw uint8 = 2
)

// Stash action identifiers for OpStashAction: 1 stores inventory into the
// position-keyed world stash, 2 takes from it.
const (
	StashActionStore uint8 = 1
	StashActionTake  uint8 = 2
)

// Trade action identifiers for OpTradeAction: 1 buys from the vendor (debits
// rubles, credits an item), 2 sells to the vendor (removes an item, credits
// rubles).
const (
	TradeActionBuy  uint8 = 1
	TradeActionSell uint8 = 2
)

// AIStateEntry is one 19-byte AI puppet entry of OpAIState (0x007C):
// EntityID u32(0), X f32(4), Y f32(8), Z f32(12), Yaw u16(16), Anim u8(18).
// Anim values: 0=idle, 1=walk, 2=run, 3=attack, 4=death. Yaw is a full circle
// in 65536 units (0..65535, 0 = facing +Z, increasing counter-clockwise).
type AIStateEntry struct {
	EntityID uint32
	X        float32
	Y        float32
	Z        float32
	Yaw      uint16
	Anim     uint8
}

// AIStateEntryWireSize is the fixed on-wire size of one AIStateEntry.
const AIStateEntryWireSize = 19

// MaxAIStateEntries is the per-packet cap of OpAIState entities; larger online
// sets are chunked across packets like snapshots.
const MaxAIStateEntries = 32

// AIStatePacket is the OpAIState (0x007C) server->client payload: a u8 count
// followed by exactly Count 19-byte entries (1 + 19*Count bytes on the wire,
// 1 + 32*19 maximum). Entries beyond Count are not serialised.
type AIStatePacket struct {
	Count   uint8
	Entries [MaxAIStateEntries]AIStateEntry
}

// AI animation wire values used by AIStateEntry.Anim.
const (
	AIAnimIdle   uint8 = 0
	AIAnimWalk   uint8 = 1
	AIAnimRun    uint8 = 2
	AIAnimAttack uint8 = 3
	AIAnimDeath  uint8 = 4
)

// Item update result codes for OpItemUpdate and OpContainerUpdate.
const (
	ItemResultOK        uint8 = 0
	ItemResultRejected  uint8 = 1
	ItemResultCorrected uint8 = 2
)

// ContainerActionPacket is the OpContainerAction (0x007F) client->server
// payload: 88 bytes. ActionID is client-monotonic and shares the OpItemAction
// replay floor; Action is ContainerActionDeposit or ContainerActionWithdraw;
// ContainerID is the numeric world_stashes.stash_id (SQLite rowid); Count is
// the requested amount; Condition is 0-100.
type ContainerActionPacket struct {
	ActionID    uint32
	ContainerID uint32
	Action      uint8
	Section     [64]byte
	Count       uint16
	X           float32
	Y           float32
	Z           float32
	Condition   uint8
}

// ContainerUpdatePacket is the OpContainerUpdate (0x0080) server->client
// payload: 77 bytes. Result is ItemResultOK/Rejected/Corrected; Count is the
// signed delta applied to the client's container/inventory view (negative =
// removed from inventory on deposit, positive = credited on withdraw).
type ContainerUpdatePacket struct {
	ActionID    uint32
	Result      uint8
	Action      uint8
	ContainerID uint32
	Count       int16
	Section     [64]byte
	Condition   uint8
}

// ItemActionPacket is the OpItemAction (0x007D) client->server payload:
// 88 bytes. ActionID is client-monotonic; Action is ItemActionDrop or
// ItemActionPickup; ItemID is 0 for drops (server assigns the world id) and
// the world_items.id for pickups; Count is the requested stack size; X/Y/Z is
// the requested drop position or ignored for pickups; Condition is 0-100.
type ItemActionPacket struct {
	ActionID  uint32
	Action    uint8
	ItemID    uint32
	Section   [64]byte
	Count     uint16
	X         float32
	Y         float32
	Z         float32
	Condition uint8
}

// ItemUpdatePacket is the OpItemUpdate (0x007E) server->client payload:
// 89 bytes. Result is ItemResultOK, ItemResultRejected or ItemResultCorrected;
// Count is a signed delta (negative = removed from the receiver's inventory,
// positive = added); Section/position/condition are echoed from server state
// (never from the client for pickups).
type ItemUpdatePacket struct {
	ActionID  uint32
	Result    uint8
	Action    uint8
	ItemID    uint32
	Count     int16
	Section   [64]byte
	X         float32
	Y         float32
	Z         float32
	Condition uint8
}

// StashActionPacket is the OpStashAction (0x0081) client->server payload:
// 84 bytes. ActionID is client-monotonic and shares the OpItemAction replay
// floor; Action is StashActionStore or StashActionTake; X/Y/Z is the raw world
// position the client sends for the stash (the server rounds it onto the 0.5 m
// key grid); Count is the requested stack size; Condition is 0-100.
type StashActionPacket struct {
	ActionID  uint32
	Action    uint8
	X         float32
	Y         float32
	Z         float32
	Section   [64]byte
	Count     uint16
	Condition uint8
}

// StashResultPacket is the OpStashResult (0x0082) server->client payload:
// 73 bytes. Result is ItemResultOK/Rejected/Corrected; Count is the signed
// delta applied to the client's inventory view (negative = stored, positive =
// taken); Section/Condition are echoed from server state.
type StashResultPacket struct {
	ActionID  uint32
	Result    uint8
	Action    uint8
	Count     int16
	Section   [64]byte
	Condition uint8
}

// TradeActionPacket is the OpTradeAction (0x0083) client->server payload:
// 76 bytes. ActionID is client-monotonic; Action is TradeActionBuy or
// TradeActionSell; MoneyDelta is the client-asserted price (server-capped);
// Count is the requested stack size; Condition is 0-100 (used for buys, the
// consumed stack is derived for sells).
type TradeActionPacket struct {
	ActionID   uint32
	Action     uint8
	MoneyDelta uint32
	Section    [64]byte
	Count      uint16
	Condition  uint8
}

// TradeResultPacket is the OpTradeResult (0x0084) server->client payload:
// 75 bytes. Result is ItemResultOK/Rejected/Corrected; MoneyDelta is the
// signed amount actually credited (positive) or debited (negative) by the
// server, 0 on rejection; Condition is the server-derived bucket for sells
// and the credited bucket for buys.
type TradeResultPacket struct {
	ActionID   uint32
	Result     uint8
	Action     uint8
	MoneyDelta int32
	Section    [64]byte
	Condition  uint8
}

// WalletUpdatePacket is the OpWalletUpdate (0x0085) server->client payload:
// 4 bytes. Money is the character's absolute authoritative ruble balance; the
// client reconciles db.actor money to it. Sent after every money change and on
// join/character-select.
type WalletUpdatePacket struct {
	Money uint32
}

func WritePacket(w io.Writer, opcode uint16, seq uint32, flags uint8, payload interface{}) error {
	// Snapshots are the 30Hz hot path: encode them directly onto the writer
	// with fixed stack buffers instead of binary.Write reflection, so no
	// intermediate bytes.Buffer or per-entry allocations are needed.
	switch p := payload.(type) {
	case *ServerSnapshot:
		if bw, ok := w.(*bytes.Buffer); ok {
			return writeServerSnapshotBuffer(bw, opcode, seq, flags, p)
		}
		return writeServerSnapshot(w, opcode, seq, flags, p)
	case ServerSnapshot:
		if bw, ok := w.(*bytes.Buffer); ok {
			return writeServerSnapshotBuffer(bw, opcode, seq, flags, &p)
		}
		return writeServerSnapshot(w, opcode, seq, flags, &p)
	}

	var buf bytes.Buffer
	if payload != nil {
		switch p := payload.(type) {
		case *InventorySyncPayload:
			if err := writeInventorySync(&buf, p.ItemCount, p.Items[:]); err != nil {
				return err
			}
		case InventorySyncPayload:
			if err := writeInventorySync(&buf, p.ItemCount, p.Items[:]); err != nil {
				return err
			}
		case *GroupState:
			if err := writeGroupState(&buf, p); err != nil {
				return err
			}
		case GroupState:
			if err := writeGroupState(&buf, &p); err != nil {
				return err
			}
		case *AIStatePacket:
			if err := writeAIState(&buf, p); err != nil {
				return err
			}
		case AIStatePacket:
			if err := writeAIState(&buf, &p); err != nil {
				return err
			}
		default:
			if err := binary.Write(&buf, binary.LittleEndian, payload); err != nil {
				return err
			}
		}
	}

	if buf.Len() > MaxSafeUDPPacketSize {
		return ErrPayloadExceedsMTU
	}

	if buf.Len() > 65535 {
		return ErrPayloadTooLarge
	}

	hdr := PacketHeader{
		Magic:         HeaderMagic,
		Protocol:      ProtocolVer,
		FlagsChannel:  flags,
		SequenceNum:   seq,
		Opcode:        opcode,
		PayloadLength: uint16(buf.Len()),
	}

	if err := binary.Write(w, binary.LittleEndian, &hdr); err != nil {
		return err
	}

	_, err := w.Write(buf.Bytes())
	return err
}

// writeServerSnapshotBuffer is writeServerSnapshot specialised for a concrete
// bytes.Buffer destination; all encode scratch stays on the stack.
func writeServerSnapshotBuffer(w *bytes.Buffer, opcode uint16, seq uint32, flags uint8, snap *ServerSnapshot) error {
	count := 0
	if snap != nil {
		count = int(snap.Count)
		if count > len(snap.Entries) {
			count = len(snap.Entries)
		}
		if count < 0 {
			count = 0
		}
	}

	payloadLen := 1 + count*SnapshotEntryWireSize
	if payloadLen > MaxSafeUDPPacketSize {
		return ErrPayloadExceedsMTU
	}

	var hdr [PacketHeaderWireSize]byte
	putHeader(hdr[:], opcode, seq, flags, uint16(payloadLen))
	_, _ = w.Write(hdr[:])
	_ = w.WriteByte(uint8(count))

	var entry [SnapshotEntryWireSize]byte
	for i := 0; i < count; i++ {
		encodeSnapshotEntry(entry[:], &snap.Entries[i])
		_, _ = w.Write(entry[:])
	}
	return nil
}

// writeServerSnapshot encodes one OpServerSnapshot packet (header + count +
// count entries) with no heap allocations. It writes at most MaxSnapshotEntries
// entries; the caller is responsible for splitting larger sets across packets.
func writeServerSnapshot(w io.Writer, opcode uint16, seq uint32, flags uint8, snap *ServerSnapshot) error {
	count := 0
	if snap != nil {
		count = int(snap.Count)
		if count > len(snap.Entries) {
			count = len(snap.Entries)
		}
		if count < 0 {
			count = 0
		}
	}

	payloadLen := 1 + count*SnapshotEntryWireSize
	if payloadLen > MaxSafeUDPPacketSize {
		return ErrPayloadExceedsMTU
	}
	if payloadLen > 65535 {
		return ErrPayloadTooLarge
	}

	var hdr [PacketHeaderWireSize]byte
	putHeader(hdr[:], opcode, seq, flags, uint16(payloadLen))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}

	var countBuf [1]byte
	countBuf[0] = uint8(count)
	if _, err := w.Write(countBuf[:]); err != nil {
		return err
	}

	var entry [SnapshotEntryWireSize]byte
	for i := 0; i < count; i++ {
		encodeSnapshotEntry(entry[:], &snap.Entries[i])
		if _, err := w.Write(entry[:]); err != nil {
			return err
		}
	}
	return nil
}

// putHeader serialises a PacketHeader into dst (must be PacketHeaderWireSize bytes).
func putHeader(dst []byte, opcode uint16, seq uint32, flags uint8, payloadLen uint16) {
	binary.LittleEndian.PutUint16(dst[0:2], HeaderMagic)
	dst[2] = ProtocolVer
	dst[3] = flags
	binary.LittleEndian.PutUint32(dst[4:8], seq)
	binary.LittleEndian.PutUint16(dst[8:10], opcode)
	binary.LittleEndian.PutUint16(dst[10:12], payloadLen)
}

// encodeSnapshotEntry writes one SnapshotEntry into dst (22 bytes).
func encodeSnapshotEntry(dst []byte, e *SnapshotEntry) {
	binary.LittleEndian.PutUint32(dst[0:4], e.SessionID)
	binary.LittleEndian.PutUint32(dst[4:8], math.Float32bits(e.PosX))
	binary.LittleEndian.PutUint32(dst[8:12], math.Float32bits(e.PosY))
	binary.LittleEndian.PutUint32(dst[12:16], math.Float32bits(e.PosZ))
	binary.LittleEndian.PutUint16(dst[16:18], uint16(e.Yaw))
	binary.LittleEndian.PutUint16(dst[18:20], uint16(e.Pitch))
	dst[20] = e.AnimFlags
	dst[21] = e.Health
}

func writeInventorySync(w io.Writer, itemCount uint8, items []InventoryItemPayload) error {
	if err := binary.Write(w, binary.LittleEndian, itemCount); err != nil {
		return err
	}
	count := int(itemCount)
	if count > len(items) {
		count = len(items)
	}
	if count == 0 {
		return nil
	}
	return binary.Write(w, binary.LittleEndian, items[:count])
}

// writeGroupState serialises only the populated member entries instead of the
// fixed [8] backing array, so the payload is 1 + 53*MemberCount bytes.
func writeGroupState(w io.Writer, state *GroupState) error {
	if state == nil {
		return nil
	}
	count := int(state.MemberCount)
	if count > len(state.Members) {
		count = len(state.Members)
	}
	if count > MaxGroupMembers {
		count = MaxGroupMembers
	}
	if err := binary.Write(w, binary.LittleEndian, uint8(count)); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	return binary.Write(w, binary.LittleEndian, state.Members[:count])
}

// ReadGroupState decodes an OpGroupState payload with its variable-length
// member list.
func ReadGroupState(r io.Reader) (*GroupState, error) {
	var state GroupState
	if err := binary.Read(r, binary.LittleEndian, &state.MemberCount); err != nil {
		return nil, err
	}
	count := int(state.MemberCount)
	if count > len(state.Members) {
		count = len(state.Members)
	}
	for i := 0; i < count; i++ {
		if err := binary.Read(r, binary.LittleEndian, &state.Members[i]); err != nil {
			return nil, err
		}
	}
	return &state, nil
}

func WriteServerSnapshot(w io.Writer, seq uint32, flags uint8, snap *ServerSnapshot) error {
	return WritePacket(w, OpServerSnapshot, seq, flags, snap)
}

func ReadServerSnapshot(r io.Reader) (*ServerSnapshot, error) {
	if br, ok := r.(*bytes.Reader); ok {
		return readServerSnapshotReader(br)
	}

	var snap ServerSnapshot
	var countBuf [1]byte
	if _, err := io.ReadFull(r, countBuf[:]); err != nil {
		return nil, err
	}
	snap.Count = countBuf[0]
	count := int(snap.Count)
	if count > len(snap.Entries) {
		count = len(snap.Entries)
	}
	var entry [SnapshotEntryWireSize]byte
	for i := 0; i < count; i++ {
		if _, err := io.ReadFull(r, entry[:]); err != nil {
			return nil, err
		}
		snap.Entries[i] = decodeSnapshotEntry(entry[:])
	}
	return &snap, nil
}

// writeAIState serialises only the populated AI entries instead of the fixed
// [32] backing array, so the payload is 1 + 19*Count bytes. Count is clamped
// to MaxAIStateEntries.
func writeAIState(w io.Writer, state *AIStatePacket) error {
	count := 0
	if state != nil {
		count = int(state.Count)
		if count > MaxAIStateEntries {
			count = MaxAIStateEntries
		}
		if count < 0 {
			count = 0
		}
	}
	if 1+count*AIStateEntryWireSize > MaxSafeUDPPacketSize {
		return ErrPayloadExceedsMTU
	}
	if err := binary.Write(w, binary.LittleEndian, uint8(count)); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	return binary.Write(w, binary.LittleEndian, state.Entries[:count])
}

// ReadAIStatePacket decodes an OpAIState payload with its variable-length entry
// list. A count above MaxAIStateEntries is clamped defensively.
func ReadAIStatePacket(r io.Reader) (*AIStatePacket, error) {
	var state AIStatePacket
	if err := binary.Read(r, binary.LittleEndian, &state.Count); err != nil {
		return nil, err
	}
	count := int(state.Count)
	if count > MaxAIStateEntries {
		count = MaxAIStateEntries
	}
	for i := 0; i < count; i++ {
		if err := binary.Read(r, binary.LittleEndian, &state.Entries[i]); err != nil {
			return nil, err
		}
	}
	return &state, nil
}

// readServerSnapshotReader is ReadServerSnapshot specialised for a concrete
// bytes.Reader; decode scratch stays on the stack.
func readServerSnapshotReader(r *bytes.Reader) (*ServerSnapshot, error) {
	var snap ServerSnapshot
	b, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	snap.Count = b
	count := int(b)
	if count > len(snap.Entries) {
		count = len(snap.Entries)
	}
	var entry [SnapshotEntryWireSize]byte
	for i := 0; i < count; i++ {
		n, err := r.Read(entry[:])
		if err != nil {
			return nil, err
		}
		if n < SnapshotEntryWireSize {
			return nil, io.ErrUnexpectedEOF
		}
		snap.Entries[i] = decodeSnapshotEntry(entry[:])
	}
	return &snap, nil
}

// decodeSnapshotEntry reads one 22-byte SnapshotEntry from src.
func decodeSnapshotEntry(src []byte) SnapshotEntry {
	return SnapshotEntry{
		SessionID: binary.LittleEndian.Uint32(src[0:4]),
		PosX:      math.Float32frombits(binary.LittleEndian.Uint32(src[4:8])),
		PosY:      math.Float32frombits(binary.LittleEndian.Uint32(src[8:12])),
		PosZ:      math.Float32frombits(binary.LittleEndian.Uint32(src[12:16])),
		Yaw:       int16(binary.LittleEndian.Uint16(src[16:18])),
		Pitch:     int16(binary.LittleEndian.Uint16(src[18:20])),
		AnimFlags: src[20],
		Health:    src[21],
	}
}

func ReadHeader(r io.Reader) (*PacketHeader, error) {
	var hdr PacketHeader
	err := binary.Read(r, binary.LittleEndian, &hdr)
	return &hdr, err
}
