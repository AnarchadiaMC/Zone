#include "../net/udp_client.h"
#include "../protocol/packets.h"
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#include <atomic>
#include <cstdint>
#include <cstring>
#include <string>
#include <cmath>

namespace
{
std::string g_SelectIP = "127.0.0.1";
uint16_t g_SelectPort = 27015;
std::atomic<uint32_t> g_SelectSeq{1};
}

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
        g_SelectIP = safeIp;
        g_SelectPort = safePort;
    }

    __declspec(dllexport) void ZN_Disconnect()
    {
        NetClient::Disconnect();
    }

    __declspec(dllexport) void ZN_SendTransform(float x, float y, float z, int16_t yaw, int16_t pitch, uint8_t animflags, uint16_t gvid)
    {
        NetClient::Transform transform;
        transform.x = x;
        transform.y = y;
        transform.z = z;
        transform.yaw = yaw;
        transform.pitch = pitch;
        transform.vx = 0;
        transform.vy = 0;
        transform.vz = 0;
        transform.animflags = animflags;
        NetClient::SendTransform(transform, gvid);
    }

    __declspec(dllexport) void ZN_SendLevelChange(const char* level)
    {
        NetClient::SendLevelChange(level ? level : "");
    }

    __declspec(dllexport) void ZN_SendPlayerVisual(const char* visual)
    {
        NetClient::SendPlayerVisual(visual ? visual : "");
    }

    __declspec(dllexport) int ZN_QueryServer(const char* ip, int port, char* out, int outLen, int timeoutMs)
    {
        if (!ip || port <= 0 || port > 65535)
            return -1;
        return NetClient::QueryServer(ip, static_cast<uint16_t>(port), out, outLen, timeoutMs);
    }

    __declspec(dllexport) bool ZN_PollEvent(char* buf, size_t* len)
    {
        if (!buf || !len)
        {
            return false;
        }
        // PRODUCTION FIX: pass explicit 1500 capacity (g_poll_buf size) instead
        // of the stale *len value, which holds the previous packet's length on
        // the Lua side. Prevents false drops and documents the contract.
        size_t got = 0;
        if (!NetClient::PollEvent(buf, 1500, got))
        {
            return false;
        }
        *len = got;
        return true;
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

    __declspec(dllexport) const char* ZN_GetLastError()
    {
        // thread_local cache: GetLastError() returns std::string by value, so
        // returning .c_str() of a temporary would dangle. The cache lives as
        // long as the calling (Lua main) thread; each call refreshes it.
        thread_local std::string cache;
        cache = NetClient::GetLastError();
        return cache.c_str();
    }

    __declspec(dllexport) bool ZN_IsPendingCreate()
    {
        return NetClient::IsPendingCreate();
    }

    __declspec(dllexport) bool ZN_HasCharacter()
    {
        return NetClient::HasCharacter();
    }

    __declspec(dllexport) void ZN_GetSpawn(float* x, float* y, float* z, uint8_t* level, uint8_t* ecoTier)
    {
        NetClient::Spawn s = NetClient::GetSpawn();
        if (x) *x = s.x;
        if (y) *y = s.y;
        if (z) *z = s.z;
        if (level) *level = s.level;
        if (ecoTier) *ecoTier = s.ecoTier;
    }

    __declspec(dllexport) const char* ZN_GetFaction()
    {
        thread_local std::string cache;
        cache = NetClient::GetFaction();
        return cache.c_str();
    }

    __declspec(dllexport) bool ZN_SendCharacterSelect(const char* faction, double money, const char* items_str)
    {
        if (!NetClient::IsConnected())
        {
            return false;
        }
        if (!std::isfinite(money) || money < 0.0 || money > static_cast<double>(UINT32_MAX))
        {
            return false;
        }
        uint32_t safeMoney = static_cast<uint32_t>(money);
        uint32_t sessionID = 0;
        uint32_t ping = 0;
        NetClient::GetSessionInfo(sessionID, ping);
        if (sessionID == 0)
        {
            return false;
        }
        NetClient::SendCharacterSelect(faction ? faction : "", safeMoney, items_str ? items_str : "");
        return true;
    }

}