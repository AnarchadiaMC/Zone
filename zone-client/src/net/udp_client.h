#pragma once
#include <string>
#include <cstdint>

namespace NetClient
{
    void Init();
    void Shutdown();

    void Connect(const std::string& ip, uint16_t port, const std::string& uuid, uint32_t hwid, const std::string& nick);
    void Disconnect();

    struct Transform {
        float x = 0.0f;
        float y = 0.0f;
        float z = 0.0f;
        int16_t yaw = 0;
        int16_t pitch = 0;
        int16_t vx = 0;
        int16_t vy = 0;
        int16_t vz = 0;
        uint16_t animflags = 0;
    };

    void SendTransform(const Transform& transform, uint16_t gvid);
    void SendLevelChange(const std::string& level);
    void SendPlayerVisual(const std::string& visual);
    bool PollEvent(char* outBuf, size_t maxLen, size_t& outLen);
    bool PollEvent(char* outBuf, size_t& outLen);
    bool PollEvent(int a, int& b);
    uint64_t GetDroppedPackets();
    
    bool IsConnected();
    bool IsInSafeZone();
    struct Spawn { float x = 0.0f; float y = 0.0f; float z = 0.0f; uint8_t level = 0; uint8_t ecoTier = 0; };
    bool IsPendingCreate();
    bool HasCharacter();
    Spawn GetSpawn();
    std::string GetFaction();
    void GetSessionInfo(uint32_t& outSessionID, uint32_t& outPing);
    std::string GetLastError();
    void SendChatText(const std::string& text);
    bool SendCharacterSelect(const std::string& faction, uint32_t money, const std::string& items);

    // One-shot server query on a dedicated temporary UDP socket. Does not touch
    // the session socket or the event ring. Sends OpServerQuery and waits up to
    // timeoutMs for OpServerQueryRes. Copies the raw payload into outBuf and
    // returns the payload byte count, or a negative error:
    //   -1 invalid argument / WSA startup / socket failure
    //   -2 sendto failed
    //   -3 timed out waiting for a matching response
    //   -4 response payload larger than outBufLen
    int QueryServer(const std::string& ip, uint16_t port, char* outBuf, int outBufLen, int timeoutMs);
}
