package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
)

type HandshakeReq struct {
	UUID        [36]byte
	HWIDHash    [4]byte
	Nickname    [32]byte
	ProtocolVer uint8
}

type HandshakeRes struct {
	SessionID [4]byte
	Status    uint8
	SpawnX    float32
	SpawnY    float32
	SpawnZ    float32
	WorldTime uint64
	EcoTier   uint8
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
	Entries [64]SnapshotEntry
}

type EntityEnterAoI struct {
	EntityID   uint32
	EntityType uint8
	Section    [32]byte
	PosX       float32
	PosY       float32
	PosZ       float32
	Faction    [16]byte
	Health     uint8
}

type EntityLeaveAoI struct {
	EntityID uint32
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
	Status  uint8     // 0=OK, 1=NotFound, 2=Error
	Count   uint16
	Data    [256]byte // JSON-serialised stash contents on Action=1 (Open)
}

// AIActionPayload carries an AI squad state update broadcast to nearby clients.
type AIActionPayload struct {
	SquadID uint32
	State   uint8
	PosX    float32
	PosY    float32
	PosZ    float32
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

func WritePacket(w io.Writer, op uint16, seq uint32, flags uint8, payload interface{}) error {
	var buf bytes.Buffer
	if payload != nil {
		if err := binary.Write(&buf, binary.LittleEndian, payload); err != nil {
			return err
		}
	}
	
	hdr := PacketHeader{
		Magic:         HeaderMagic,
		Protocol:      ProtocolVer,
		FlagsChannel:  flags,
		SequenceNum:   seq,
		Opcode:        op,
		PayloadLength: uint16(buf.Len()),
	}

	if err := binary.Write(w, binary.LittleEndian, &hdr); err != nil {
		return err
	}
	
	_, err := w.Write(buf.Bytes())
	return err
}

func ReadHeader(r io.Reader) (*PacketHeader, error) {
	var hdr PacketHeader
	err := binary.Read(r, binary.LittleEndian, &hdr)
	return &hdr, err
}
