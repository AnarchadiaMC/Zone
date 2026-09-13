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
    AI_ACTION_EVENT = 0x0070
};

struct HeartbeatPayload {
    uint64_t timestamp;
};

struct HandshakeReq {
    char uuid[36];
    uint32_t hwid;
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
};

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
};

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

struct ServerSnapshot {
    uint8_t count;
    SnapshotEntry entries[32];
};

struct SafeZoneState {
    uint8_t locked;
    char zoneID[32];
};

struct WorldEvent {
    uint8_t eventType;
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
    char section[32];
    float posX;
    float posY;
    float posZ;
    char faction[16];
    uint8_t health;
};

struct EntityLeaveAoI {
    uint32_t entityID;
};

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

#pragma pack(pop)
