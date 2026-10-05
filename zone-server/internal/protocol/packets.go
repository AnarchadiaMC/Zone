package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

const (
	MaxSafeUDPPacketSize        = 1200
	OpAck                uint16 = 0x0005
)

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
	Entries [32]SnapshotEntry
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

// StashInteractPayload — client sends when opening/taking/storing at a stash box.
// Action: 1=Open, 2=Take, 3=Store
type StashInteractPayload struct {
	StashID     uint32
	Action      uint8    // 1=Open, 2=Take, 3=Store
	ItemSection [32]byte // null-terminated item section string
	Count       uint16
}

// StashResponsePayload — server reply to a stash interaction.
// Status: 0=OK, 1=NotFound, 2=Error
type StashResponsePayload struct {
	StashID uint32
	Status  uint8 // 0=OK, 1=NotFound, 2=Error
	Count   uint16
	Data    [256]byte // JSON-serialised stash contents on Action=1 (Open)
}

// AIActionPayload carries an AI squad state update broadcast to nearby clients.
type AIActionPayload struct {
	EntityID uint32
	Action   uint8
	TargetID uint32
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

// GroupStateMember is one member entry of OpGroupState: 49 bytes.
type GroupStateMember struct {
	SessionID uint32
	Name      [32]byte
	Faction   [16]byte
	IsLeader  uint8
}

// GroupState is the OpGroupState (0x007A) server->client payload. The wire
// form carries MemberCount followed by exactly MemberCount member entries
// (1 + 49 each); an empty state is a single 0x00 byte.
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

func WritePacket(w io.Writer, opcode uint16, seq uint32, flags uint8, payload interface{}) error {
	var buf bytes.Buffer
	if payload != nil {
		switch p := payload.(type) {
		case *ServerSnapshot:
			if err := binary.Write(&buf, binary.LittleEndian, p.Count); err != nil {
				return err
			}
			count := int(p.Count)
			if count > len(p.Entries) {
				count = len(p.Entries)
			}
			if count > 0 {
				if err := binary.Write(&buf, binary.LittleEndian, p.Entries[:count]); err != nil {
					return err
				}
			}
		case ServerSnapshot:
			if err := binary.Write(&buf, binary.LittleEndian, p.Count); err != nil {
				return err
			}
			count := int(p.Count)
			if count > len(p.Entries) {
				count = len(p.Entries)
			}
			if count > 0 {
				if err := binary.Write(&buf, binary.LittleEndian, p.Entries[:count]); err != nil {
					return err
				}
			}
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
// fixed [8] backing array, so the payload is 1 + 49*MemberCount bytes.
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
	var snap ServerSnapshot
	if err := binary.Read(r, binary.LittleEndian, &snap.Count); err != nil {
		return nil, err
	}
	count := int(snap.Count)
	if count > len(snap.Entries) {
		count = len(snap.Entries)
	}
	for i := 0; i < count; i++ {
		if err := binary.Read(r, binary.LittleEndian, &snap.Entries[i]); err != nil {
			return nil, err
		}
	}
	return &snap, nil
}

func ReadHeader(r io.Reader) (*PacketHeader, error) {
	var hdr PacketHeader
	err := binary.Read(r, binary.LittleEndian, &hdr)
	return &hdr, err
}
