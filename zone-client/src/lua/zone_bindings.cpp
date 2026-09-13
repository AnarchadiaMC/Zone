#include "../net/udp_client.h"
#include <windows.h>
#include <cstdint>

extern "C" {

    __declspec(dllexport) void ZN_Connect(const char* ip, double port, const char* uuid, double hwid, const char* nick)
    {
        const char* safeIp = ip ? ip : "127.0.0.1";
        const char* safeUuid = uuid ? uuid : "";
        const char* safeNick = nick ? nick : "Stalker";

        uint16_t safePort = 27015;
        if (port > 0.0 && port <= 65535.0)
        {
            safePort = static_cast<uint16_t>(port);
        }

        uint32_t safeHwid = 0;
        if (hwid >= 0.0 && hwid <= static_cast<double>(UINT32_MAX))
        {
            safeHwid = static_cast<uint32_t>(hwid);
        }

        NetClient::Connect(safeIp, safePort, safeUuid, safeHwid, safeNick);
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
        if (!buf || !len)
        {
            return false;
        }
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
        uint32_t sID = 0;
        uint32_t p = 0;
        NetClient::GetSessionInfo(sID, p);
        if (sessionID) *sessionID = sID;
        if (ping) *ping = p;
    }

    __declspec(dllexport) void ZN_SendChatText(const char* text)
    {
        NetClient::SendChatText(text ? text : "");
    }

}