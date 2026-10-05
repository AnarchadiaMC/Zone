#pragma once
#include <cstdint>

#pragma pack(push, 1)

struct ZO_Header {
    uint16_t magic;        // 0x5A4F
    uint8_t  protocol;     // 0x01
    uint8_t  flags;        // 0x01=reliable, 0x02=unreliable, 0x04=compressed
    uint32_t sequence;     // monotonic counter
    uint16_t opcode;
    uint16_t payload_len;
};

enum class Opcode : uint16_t {
    HANDSHAKE_REQ = 0x0001, 
    HANDSHAKE_RES = 0x0002, 
    DISCONNECT = 0x0003, 
    HEARTBEAT = 0x0004,
    ACK = 0x0005,
    SERVER_QUERY = 0x0006,
    SERVER_QUERY_RES = 0x0007,
    CLIENT_TRANSFORM = 0x0010, 
    SERVER_SNAPSHOT = 0x0011,
    ENTITY_ENTER_AOI = 0x0012, 
    ENTITY_LEAVE_AOI = 0x0013,
    SAFEZONE_STATE = 0x0020,
    WORLD_EVENT = 0x0030,
    STASH_INTERACT = 0x0040, 
    STASH_RESPONSE = 0x0041,
    DAMAGE_NOTIFY = 0x0050,
    CHAT_TEXT = 0x0060,
    AI_ACTION_EVENT = 0x0070,
    SHOW_START = 0x0071,
    CHARACTER_SELECT = 0x0072,
    LOAD_LEVEL = 0x0073,
    LEVEL_CHANGE = 0x0074,
    PLAYER_VISUAL = 0x0075,
    GROUP_INVITE_NOTIFY = 0x0078,
    GROUP_RESPONSE = 0x0079,
    GROUP_STATE = 0x007A
};

struct HeartbeatPayload {
    uint64_t timestamp;
};

struct AckPayload {
    uint32_t seq;
};

struct HandshakeReq {
    char uuid[37];
    uint8_t hwid[32]; // SHA-256 binary hash
    char nick[32];
    uint8_t protoVer;
};

struct HandshakeRes {
    uint32_t sessionID;
    uint8_t status;
    float spawnX;
    float spawnY;
    float spawnZ;
    uint64_t worldTime;
    uint8_t ecoTier;
    uint8_t hasCharacter;
    char faction[16];
};

struct ServerQueryRes {
    char name[32];
    char map[32];
    uint8_t players;
    uint8_t maxPlayers;
    uint8_t mode;
    uint8_t locked;
    uint8_t protoVer;
    uint8_t tickRateHz;
};
static_assert(sizeof(ServerQueryRes) == 70, "ServerQueryRes size must be exactly 70 bytes");

struct ClientTransform {
    uint32_t sessionID;
    float posX;
    float posY;
    float posZ;
    int16_t yaw;
    int16_t pitch;
    int16_t velX;
    int16_t velY;
    int16_t velZ;
    uint8_t animFlags;
    uint16_t gvid;         // appended last (protocol v2)
};
static_assert(sizeof(ClientTransform) == 29, "ClientTransform size must be exactly 29 bytes");

struct SnapshotEntry {
    uint32_t sessionID;
    float posX;
    float posY;
    float posZ;
    int16_t yaw;
    int16_t pitch;
    uint8_t animFlags;
    uint8_t health;
};
static_assert(sizeof(SnapshotEntry) == 22, "SnapshotEntry size must be exactly 22 bytes");

struct ServerSnapshot {
    uint8_t count;
    SnapshotEntry entries[32];
};

struct SafeZoneState {
    uint8_t locked;
    char zoneID[32];
};
static_assert(sizeof(SafeZoneState) == 33, "SafeZoneState size must be exactly 33 bytes");

struct WorldEvent {
    uint8_t eventType;
    uint8_t state;
    uint32_t timer;
};

struct ChatText {
    uint32_t senderID;
    uint8_t len;
    char text[255];
};

struct EntityEnterAoI {
    uint32_t entityID;
    uint8_t type;
    char section[64];
    float posX;
    float posY;
    float posZ;
    char faction[16];
    uint8_t health;
    uint16_t gvid;
    char name[32];         // appended last (protocol v3)
};
static_assert(sizeof(EntityEnterAoI) == 132, "EntityEnterAoI size must be exactly 132 bytes");

struct GroupInviteNotify {
    uint32_t inviterSessionID;
    char inviterName[32];
    char inviterFaction[16];
};
static_assert(sizeof(GroupInviteNotify) == 52, "GroupInviteNotify size must be exactly 52 bytes");

struct GroupResponse {
    uint8_t accept;
};
static_assert(sizeof(GroupResponse) == 1, "GroupResponse size must be exactly 1 byte");

struct GroupStateHeader {
    uint8_t count;
};
static_assert(sizeof(GroupStateHeader) == 1, "GroupStateHeader size must be exactly 1 byte");

struct GroupStateMember {
    uint32_t sessionID;
    char name[32];
    char faction[16];
    uint8_t isLeader;
};
static_assert(sizeof(GroupStateMember) == 53, "GroupStateMember size must be exactly 53 bytes");

struct EntityLeaveAoI {
    uint32_t entityID;
};

struct LevelChange {
    char level[32];
};
static_assert(sizeof(LevelChange) == 32, "LevelChange size must be exactly 32 bytes");

struct PlayerVisual {
    char visual[64];
};
static_assert(sizeof(PlayerVisual) == 64, "PlayerVisual size must be exactly 64 bytes");

struct AIActionEvent {
    uint32_t entityID;
    uint8_t action;
    uint32_t targetID;
};

struct DamageNotify {
    uint32_t targetID;
    uint32_t attackerID;
    float damage;
    uint8_t boneID;
};

struct ShowStart {
    uint8_t level;
    float posX;
    float posY;
    float posZ;
    uint8_t flags;
    uint8_t ecoTier;
};

struct CharacterSelect {
    char faction[16];
    uint32_t money;
    char items[256];
};

#pragma pack(pop)
