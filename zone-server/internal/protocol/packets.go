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
