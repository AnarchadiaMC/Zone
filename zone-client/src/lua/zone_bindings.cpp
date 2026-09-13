#include "../net/udp_client.h"
#include <windows.h>
#include <cstdint>

extern "C" {

    __declspec(dllexport) void ZN_Connect(const char* ip, double port, const char* uuid, double hwid, const char* nick)
    {
        NetClient::Connect(ip, (uint16_t)port, uuid, (uint32_t)hwid, nick);
    }

    __declspec(dllexport) void ZN_Disconnect()
    {
        NetClient::Disconnect();
    }

    __declspec(dllexport) void ZN_SendTransform(float x, float y, float z, int16_t yaw, int16_t pitch, int16_t vx, int16_t vy, int16_t vz, uint8_t animflags)
    {
        NetClient::Transform transform;
        transform.x = x;
        transform.y = y;
        transform.z = z;
        transform.yaw = yaw;
        transform.pitch = pitch;
        transform.vx = vx;
        transform.vy = vy;
        transform.vz = vz;
        transform.animflags = animflags;
        NetClient::SendTransform(transform);
    }

    __declspec(dllexport) bool ZN_PollEvent(char* buf, size_t* len)
    {
        return NetClient::PollEvent(buf, *len);
    }

    __declspec(dllexport) bool ZN_IsInSafeZone()
    {
        return NetClient::IsInSafeZone();
    }

    __declspec(dllexport) bool ZN_IsConnected()
    {
        return NetClient::IsConnected();
    }

    __declspec(dllexport) void ZN_GetSessionInfo(uint32_t* sessionID, uint32_t* ping)
    {
        NetClient::GetSessionInfo(*sessionID, *ping);
    }

    __declspec(dllexport) void ZN_SendChatText(const char* text)
    {
        NetClient::SendChatText(text ? text : "");
    }

}