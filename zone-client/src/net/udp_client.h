#pragma once
#include <string>
#include <cstdint>

namespace NetClient
{
    void Init();
    void Shutdown();

    void Connect(const std::string& ip, uint16_t port, const std::string& uuid, uint32_t hwid, const std::string& nick);
    void Disconnect();
    void SendTransform(float x, float y, float z, int16_t yaw, int16_t pitch, int16_t vx, int16_t vy, int16_t vz, uint16_t animflags);
    bool PollEvent(char* outBuf, size_t& outLen);
    
    bool IsConnected();
    bool IsInSafeZone();
    void GetSessionInfo(uint32_t& outSessionID, uint32_t& outPing);
    std::string GetLastError();
    void SendChatText(const std::string& text);
}
