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

    void SendTransform(const Transform& transform);
    bool PollEvent(char* outBuf, size_t maxLen, size_t& outLen);
    bool PollEvent(char* outBuf, size_t& outLen);
    bool PollEvent(int a, int& b);
    uint64_t GetDroppedPackets();
    
    bool IsConnected();
    bool IsInSafeZone();
    void GetSessionInfo(uint32_t& outSessionID, uint32_t& outPing);
    std::string GetLastError();
    void SendChatText(const std::string& text);
}
