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
	OpHandshakeReq      = 0x0001
	OpHandshakeRes      = 0x0002
	OpDisconnect        = 0x0003
	OpHeartbeat         = 0x0004
	OpServerQuery       = 0x0006
	OpServerQueryRes    = 0x0007
	OpClientTransform   = 0x0010
	OpServerSnapshot    = 0x0011
	OpEntityEnterAoI    = 0x0012
	OpEntityLeaveAoI    = 0x0013
	OpSafezoneState     = 0x0020
	OpWorldEvent        = 0x0030
	OpDamageNotify      = 0x0050
	OpChatText          = 0x0060
	OpShowStart         = 0x0071
	OpCharacterSelect   = 0x0072
	OpLoadLevel         = 0x0073
	OpLevelChange       = 0x0074
	OpPlayerVisual      = 0x0075
	OpInventorySync     = 0x0076
	OpError             = 0x0077
	OpGroupInviteNotify = 0x0078
	OpGroupResponse     = 0x0079
	OpGroupState        = 0x007A
	OpAIState           = 0x007C
	OpItemAction        = 0x007D
	OpItemUpdate        = 0x007E
	OpContainerAction   = 0x007F
	OpContainerUpdate   = 0x0080
	OpStashAction       = 0x0081
	OpStashResult       = 0x0082
	OpTradeAction       = 0x0083
	OpTradeResult       = 0x0084
	OpWalletUpdate      = 0x0085
)

type PacketHeader struct {
	Magic         uint16
	Protocol      uint8
	FlagsChannel  uint8
	SequenceNum   uint32
	Opcode        uint16
	PayloadLength uint16
}
