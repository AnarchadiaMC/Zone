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
	AnimFlags uint16
}

type SnapshotEntry struct {
	SessionID uint32
	PosX      float32
	PosY      float32
	PosZ      float32
	Yaw       int16
	Pitch     int16
	AnimFlags uint16
	Health    uint8
}

type ServerSnapshot struct {
	Count   uint8
	Entries [64]SnapshotEntry
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
