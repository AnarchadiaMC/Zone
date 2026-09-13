package protocol

const (
	HeaderMagic = 0x5A4F
	ProtocolVer = 0x01
)

const (
	FlagReliable   = 0x01
	FlagUnreliable = 0x02
	FlagCompressed = 0x04
)

const (
	OpHandshakeReq   = 0x0001
	OpHandshakeRes   = 0x0002
	OpDisconnect     = 0x0003
	OpHeartbeat      = 0x0004
	OpClientTransform = 0x0010
	OpServerSnapshot  = 0x0011
	OpEntityEnterAoI  = 0x0012
	OpEntityLeaveAoI  = 0x0013
	OpSafezoneState   = 0x0020
	OpWorldEvent      = 0x0030
	OpStashInteract   = 0x0040
	OpStashResponse   = 0x0041
	OpDamageNotify    = 0x0050
	OpChatText        = 0x0060
	OpAIActionEvent   = 0x0070
)

type PacketHeader struct {
	Magic         uint16
	Protocol      uint8
	FlagsChannel  uint8
	SequenceNum   uint32
	Opcode        uint16
	PayloadLength uint16
}
