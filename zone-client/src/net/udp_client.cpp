#include "udp_client.h"
#include "../protocol/packets.h"
#include "../identity/identity.h"
#include <winsock2.h>
#include <ws2tcpip.h>
#include <thread>
#include <atomic>
#include <mutex>
#include <chrono>
#include <vector>
#include <algorithm>
#include <condition_variable>
#include <iostream>

#pragma comment(lib, "ws2_32.lib")

namespace
{
    std::atomic<bool> g_Running = false;
    std::thread g_NetThread;
    
    SOCKET g_Socket = INVALID_SOCKET;
    sockaddr_in g_ServerAddr = {};
    
    enum State { DISCONNECTED, CONNECTING, CONNECTED };
    std::atomic<State> g_State = DISCONNECTED;
    
    std::atomic<uint32_t> g_SessionID = 0;
    std::atomic<uint32_t> g_Ping = 0;
    std::atomic<bool> g_InSafeZone = false;
    std::atomic<bool> g_PendingCreate = false;
    std::atomic<bool> g_HasCharacter = false;
    float g_SpawnX = 0.0f;
    float g_SpawnY = 0.0f;
    float g_SpawnZ = 0.0f;
    uint8_t g_StartLevel = 0;
    uint8_t g_EcoTierCache = 0;
    char g_Faction[16] = {};
    
    std::string g_LastError;
    
    struct RingBufferEntry
    {
        size_t len;
        char data[1500];
    };
    RingBufferEntry g_Ring[256];
    std::atomic<uint32_t> g_RingHead = 0;
    std::atomic<uint32_t> g_RingTail = 0;
    std::atomic<uint64_t> g_DroppedPackets = 0;
    
    std::mutex g_StateMutex;
    std::condition_variable g_StateCV;
    std::mutex g_SendMutex;
    
    std::string g_TargetIP;
    uint16_t g_TargetPort = 0;
    std::string g_UUID;
    uint32_t g_HWID = 0;
    std::string g_Nick;
    
    std::atomic<uint32_t> g_Sequence = 0;

    constexpr uint8_t FlagReliable = 0x01;
    constexpr uint8_t FlagUnreliable = 0x02;

    constexpr int kMaxRawPayload = 1200;
    
    int g_ConnectRetries = 0;
    int g_ConnectBackoffMs = 1000;
    std::chrono::steady_clock::time_point g_LastConnectTime;
    std::chrono::steady_clock::time_point g_ConnectStartTime;
    std::chrono::steady_clock::time_point g_LastPacketReceivedTime;
    
    struct CachedTransform {
        float x = 0, y = 0, z = 0;
        int16_t yaw = 0, pitch = 0;
        int16_t vx = 0, vy = 0, vz = 0;
        uint16_t animflags = 0;
        uint16_t gvid = 0;
        bool updated = false;
    } g_Transform;
    std::mutex g_TransformMutex;

    // Non-blocking server query table. Each in-flight query owns a dedicated
    // temporary UDP socket plus one WSAStartup reference, released exactly once
    // when the entry is removed (poll success/failure, cancel, or expiry sweep).
    struct PendingQuery
    {
        int id = 0;
        SOCKET sock = INVALID_SOCKET;
        std::chrono::steady_clock::time_point deadline;
        std::chrono::steady_clock::time_point rttStart;
    };
    constexpr size_t kMaxPendingQueries = 32;
    std::mutex g_QueryMutex;
    std::vector<PendingQuery> g_PendingQueries;
    std::atomic<int> g_NextQueryId = 1;
    std::atomic<uint32_t> g_QuerySequence = 1;

    void CloseQuerySocket(PendingQuery& query)
    {
        if (query.sock != INVALID_SOCKET)
        {
            closesocket(query.sock);
            query.sock = INVALID_SOCKET;
        }
        WSACleanup();
    }

    // Must be called with g_QueryMutex held.
    void SweepExpiredQueries(std::chrono::steady_clock::time_point now)
    {
        auto it = g_PendingQueries.begin();
        while (it != g_PendingQueries.end())
        {
            if (now >= it->deadline)
            {
                CloseQuerySocket(*it);
                it = g_PendingQueries.erase(it);
            }
            else
            {
                ++it;
            }
        }
    }

    void SendPacket(const char* buf, int len)
    {
        if (g_Socket == INVALID_SOCKET) return;

        sockaddr_in targetAddr;
        {
            std::lock_guard<std::mutex> stateLock(g_StateMutex);
            targetAddr = g_ServerAddr;
        }

        std::lock_guard<std::mutex> sendLock(g_SendMutex);
        int res = sendto(g_Socket, buf, len, 0, (sockaddr*)&targetAddr, sizeof(targetAddr));
        if (res == SOCKET_ERROR)
        {
            int err = WSAGetLastError();
            // WSAEWOULDBLOCK is normal for non-blocking sockets.
            if (err != WSAEWOULDBLOCK)
            {
                {
                    std::lock_guard<std::mutex> stateLock(g_StateMutex);
                    g_LastError = "sendto failed: " + std::to_string(err);
                }
                if (err == WSAECONNRESET) // ICMP port unreachable
                {
                    std::lock_guard<std::mutex> stateLock(g_StateMutex);
                    g_State = DISCONNECTED;
                }
            }
        }
    }

    void BackgroundThread()
    {
        using namespace std::chrono;
        
        auto lastHeartbeat = steady_clock::now();
        auto lastTransform = steady_clock::now();
        
        while (g_Running)
        {
            auto now = steady_clock::now();
            
            if (g_State == DISCONNECTED)
            {
                std::unique_lock<std::mutex> lock(g_StateMutex);
                g_StateCV.wait(lock, [] { return !g_Running || g_State != DISCONNECTED; });
                continue;
            }
            
            if (g_State == CONNECTING)
            {
                int retries = 0;
                int backoff = 1000;
                steady_clock::time_point lastConnect;
                steady_clock::time_point connectStart;
                {
                    std::lock_guard<std::mutex> lock(g_StateMutex);
                    retries = g_ConnectRetries;
                    backoff = g_ConnectBackoffMs;
                    lastConnect = g_LastConnectTime;
                    connectStart = g_ConnectStartTime;
                }

                auto elapsedSec = duration_cast<seconds>(now - connectStart).count();
                if (retries >= 5 || elapsedSec >= 15)
                {
                    {
                        std::lock_guard<std::mutex> lock(g_StateMutex);
                        g_State = DISCONNECTED;
                        g_LastError = "Connection handshake failed: retry limit exceeded";
                    }
                    g_StateCV.notify_all();
                    continue;
                }

                if (duration_cast<milliseconds>(now - lastConnect).count() >= backoff)
                {
                    std::string uuidCopy;
                    std::string nickCopy;
                    {
                        std::lock_guard<std::mutex> lock(g_StateMutex);
                        uuidCopy = g_UUID;
                        nickCopy = g_Nick;
                        g_ConnectRetries++;
                        g_LastConnectTime = now;
                        g_ConnectBackoffMs = std::min(backoff * 2, 8000);
                    }

                    if (uuidCopy.empty())
                    {
                        uuidCopy = Identity::GetClientUUID();
                    }
                    if (nickCopy.empty())
                    {
                        nickCopy = Identity::GetNickname();
                    }

                    // Send Handshake
                    HandshakeReq req = {};
                    snprintf(req.uuid, sizeof(req.uuid), "%s", uuidCopy.c_str());
                    const uint8_t* hwidBuf = Identity::GetHWID();
                    if (hwidBuf)
                    {
                        memcpy(req.hwid, hwidBuf, sizeof(req.hwid));
                    }
                    snprintf(req.nick, sizeof(req.nick), "%s", nickCopy.c_str());
                    req.protoVer = 1;
                    
                    ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::HANDSHAKE_REQ, sizeof(req) };
                    char buf[1500];
                    memcpy(buf, &hdr, sizeof(hdr));
                    memcpy(buf + sizeof(hdr), &req, sizeof(req));
                    
                    SendPacket(buf, sizeof(hdr) + sizeof(req));
                }
            }
            else if (g_State == CONNECTED)
            {
                steady_clock::time_point lastRecv;
                {
                    std::lock_guard<std::mutex> lock(g_StateMutex);
                    lastRecv = g_LastPacketReceivedTime;
                }
                if (duration_cast<seconds>(now - lastRecv).count() >= 10)
                {
                    {
                        std::lock_guard<std::mutex> lock(g_StateMutex);
                        g_State = DISCONNECTED;
                        g_LastError = "Server connection timed out";
                        g_SessionID = 0;
                        g_SpawnX = 0.0f; g_SpawnY = 0.0f; g_SpawnZ = 0.0f;
                        g_StartLevel = 0; g_EcoTierCache = 0;
                        memset(g_Faction, 0, sizeof(g_Faction));
                    }
                    g_PendingCreate.store(false);
                    g_HasCharacter.store(false);
                    g_InSafeZone.store(false, std::memory_order_release);
                    g_StateCV.notify_all();
                    continue;
                }

                if (duration_cast<milliseconds>(now - lastHeartbeat).count() > 1000)
                {
                    uint64_t ts = (uint64_t)duration_cast<milliseconds>(system_clock::now().time_since_epoch()).count();
                    HeartbeatPayload hb = { ts };
                    ZO_Header hdr = { 0x5A4F, 1, 0, ++g_Sequence, (uint16_t)Opcode::HEARTBEAT, sizeof(hb) };
                    char buf[sizeof(hdr) + sizeof(hb)];
                    memcpy(buf, &hdr, sizeof(hdr));
                    memcpy(buf + sizeof(hdr), &hb, sizeof(hb));
                    SendPacket(buf, sizeof(buf));
                    lastHeartbeat = now;
                }
                
                if (duration_cast<milliseconds>(now - lastTransform).count() >= 33) // ~30Hz
                {
                    ClientTransform ct = {};
                    bool send = false;
                    {
                        std::lock_guard<std::mutex> lock(g_TransformMutex);
                        if (g_Transform.updated)
                        {
                            ct.posX = g_Transform.x; ct.posY = g_Transform.y; ct.posZ = g_Transform.z;
                            ct.yaw = g_Transform.yaw; ct.pitch = g_Transform.pitch;
                            ct.velX = g_Transform.vx; ct.velY = g_Transform.vy; ct.velZ = g_Transform.vz;
                            ct.animFlags = g_Transform.animflags;
                            ct.gvid = g_Transform.gvid;
                            send = true;
                        }
                    }
                    if (send)
                    {
                        ct.sessionID = g_SessionID;
                        ZO_Header hdr = { 0x5A4F, 1, 2, ++g_Sequence, (uint16_t)Opcode::CLIENT_TRANSFORM, sizeof(ct) };
                        char buf[1500];
                        memcpy(buf, &hdr, sizeof(hdr));
                        memcpy(buf + sizeof(hdr), &ct, sizeof(ct));
                        SendPacket(buf, sizeof(hdr) + sizeof(ct));
                    }
                    lastTransform = now;
                }
            }
            
            // Recv
            fd_set readfds;
            FD_ZERO(&readfds);
            FD_SET(g_Socket, &readfds);
            
            timeval tv = {0, 10000}; // 10ms
            if (select(0, &readfds, nullptr, nullptr, &tv) > 0)
            {
                sockaddr_in from;
                int fromLen = sizeof(from);
                char recvBuf[1500];
                int res = recvfrom(g_Socket, recvBuf, sizeof(recvBuf), 0, (sockaddr*)&from, &fromLen);
                if (res >= (int)sizeof(ZO_Header))
                {
                    ZO_Header* hdr = (ZO_Header*)recvBuf;
                    if (hdr->magic == 0x5A4F)
                    {
                        {
                            std::lock_guard<std::mutex> lock(g_StateMutex);
                            g_LastPacketReceivedTime = steady_clock::now();
                        }

                        if (hdr->flags & 0x01)
                        {
                            ZO_Header ackHdr = { 0x5A4F, 1, 0x02, ++g_Sequence, (uint16_t)Opcode::ACK, sizeof(uint32_t) };
                            uint32_t ackSeq = hdr->sequence;
                            char ackBuf[sizeof(ZO_Header) + sizeof(uint32_t)];
                            memcpy(ackBuf, &ackHdr, sizeof(ZO_Header));
                            memcpy(ackBuf + sizeof(ZO_Header), &ackSeq, sizeof(uint32_t));
                            SendPacket(ackBuf, sizeof(ackBuf));
                        }

                        if (hdr->opcode == (uint16_t)Opcode::HANDSHAKE_RES)
                        {
                            if (res >= (int)(sizeof(ZO_Header) + sizeof(HandshakeRes) - 17))
                            {
                                HandshakeRes* hr = (HandshakeRes*)(recvBuf + sizeof(ZO_Header));
                                // PRODUCTION FIX: honor status (0=ok, 1=full,
                                // 2=banned, 3=version mismatch). Old code set
                                // CONNECTED unconditionally, so banned players
                                // walked in and version mismatches desynced.
                                if (hr->status != 0)
                                {
                                    std::lock_guard<std::mutex> lock(g_StateMutex);
                                    switch (hr->status)
                                    {
                                    case 2: g_LastError = "Handshake rejected: banned"; break;
                                    case 1: g_LastError = "Handshake rejected: server full"; break;
                                    case 3: g_LastError = "Handshake rejected: version mismatch"; break;
                                    default: g_LastError = "Handshake rejected: status " + std::to_string(hr->status); break;
                                    }
                                    g_SessionID = 0;
                                    g_PendingCreate.store(false);
                                    g_HasCharacter.store(false);
                                    g_SpawnX = 0.0f; g_SpawnY = 0.0f; g_SpawnZ = 0.0f;
                                    g_StartLevel = 0; g_EcoTierCache = 0;
                                    memset(g_Faction, 0, sizeof(g_Faction));
                                    g_State = DISCONNECTED;
                                    g_StateCV.notify_all();
                                }
                                else
                                {
                                    g_SessionID = hr->sessionID;
                                    {
                                        std::lock_guard<std::mutex> lock(g_StateMutex);
                                        g_SpawnX = hr->spawnX; g_SpawnY = hr->spawnY; g_SpawnZ = hr->spawnZ;
                                        g_EcoTierCache = hr->ecoTier;
                                        size_t payloadLen = (size_t)res - sizeof(ZO_Header);
                                        if (payloadLen >= sizeof(HandshakeRes) - 16)
                                        {
                                            uint8_t hasChar = *(uint8_t*)((char*)hr + sizeof(HandshakeRes) - 17);
                                            g_HasCharacter.store(hasChar != 0);
                                            g_PendingCreate.store(hasChar == 0);
                                            if (payloadLen >= sizeof(HandshakeRes))
                                            {
                                                memcpy(g_Faction, (char*)hr + sizeof(HandshakeRes) - 16, 16);
                                            }
                                            else
                                            {
                                                memset(g_Faction, 0, sizeof(g_Faction));
                                            }
                                        }
                                        else
                                        {
                                            g_HasCharacter.store(true);
                                            g_PendingCreate.store(false);
                                            memset(g_Faction, 0, sizeof(g_Faction));
                                        }
                                        g_State = CONNECTED;
                                    }
                                    g_StateCV.notify_all();
                                }
                            }
                        }
                        else if (hdr->opcode == (uint16_t)Opcode::SHOW_START)
                        {
                            size_t payloadLen = (size_t)res - sizeof(ZO_Header);
                            if (payloadLen >= 15)
                            {
                                char* p = recvBuf + sizeof(ZO_Header);
                                uint8_t lvl = *(uint8_t*)(p + 0);
                                float sx, sy, sz;
                                memcpy(&sx, p + 1, 4); memcpy(&sy, p + 5, 4); memcpy(&sz, p + 9, 4);
                                uint8_t eco = *(uint8_t*)(p + 14);
                                {
                                    std::lock_guard<std::mutex> lock(g_StateMutex);
                                    g_StartLevel = lvl;
                                    g_SpawnX = sx; g_SpawnY = sy; g_SpawnZ = sz;
                                    g_EcoTierCache = eco;
                                }
                                g_PendingCreate.store(true);
                                uint32_t head = g_RingHead.load(std::memory_order_relaxed);
                                uint32_t nextHead = (head + 1) % 256;
                                if (nextHead != g_RingTail.load(std::memory_order_acquire))
                                {
                                    g_Ring[head].len = (size_t)res;
                                    memcpy(g_Ring[head].data, recvBuf, res);
                                    g_RingHead.store(nextHead, std::memory_order_release);
                                }
                                else
                                {
                                    g_DroppedPackets.fetch_add(1, std::memory_order_relaxed);
                                }
                            }
                        }
                        else if (hdr->opcode == (uint16_t)Opcode::DISCONNECT)
                        {
                            // Server kick (KickSession): drop to DISCONNECTED so
                            // Lua IsConnected() goes false, tick stops sending,
                            // and the HUD hides. Previously a kicked client sat
                            // CONNECTED until the 10s liveness timeout.
                            {
                                std::lock_guard<std::mutex> lock(g_StateMutex);
                                g_LastError = "Disconnected by server (kick)";
                                g_SessionID = 0;
                                g_SpawnX = 0.0f; g_SpawnY = 0.0f; g_SpawnZ = 0.0f;
                                g_StartLevel = 0; g_EcoTierCache = 0;
                                memset(g_Faction, 0, sizeof(g_Faction));
                                g_State = DISCONNECTED;
                            }
                            g_PendingCreate.store(false);
                            g_HasCharacter.store(false);
                            g_InSafeZone.store(false, std::memory_order_release);
                            g_StateCV.notify_all();
                        }
                        else if (hdr->opcode == (uint16_t)Opcode::SAFEZONE_STATE)
                        {
                            static_assert(sizeof(SafeZoneState) == 33, "SafeZoneState must be exactly 33 bytes");
                            if (res >= (int)(sizeof(ZO_Header) + sizeof(SafeZoneState)))
                            {
                                SafeZoneState* sz = (SafeZoneState*)(recvBuf + sizeof(ZO_Header));
                                g_InSafeZone.store(sz->locked != 0, std::memory_order_release);
                            }
                            
                            // Enqueue for Lua so zone_safezone.script receives it
                            uint32_t head = g_RingHead.load(std::memory_order_relaxed);
                            uint32_t nextHead = (head + 1) % 256;
                            if (nextHead != g_RingTail.load(std::memory_order_acquire))
                            {
                                g_Ring[head].len = (size_t)res;
                                memcpy(g_Ring[head].data, recvBuf, res);
                                g_RingHead.store(nextHead, std::memory_order_release);
                            }
                            else
                            {
                                g_DroppedPackets.fetch_add(1, std::memory_order_relaxed);
                            }
                        }
                        else if (hdr->opcode == (uint16_t)Opcode::HEARTBEAT)
                        {
                            if (res >= (int)(sizeof(ZO_Header) + sizeof(uint64_t)))
                            {
                                uint64_t sentTs = *(uint64_t*)(recvBuf + sizeof(ZO_Header));
                                uint64_t nowMs = (uint64_t)duration_cast<milliseconds>(system_clock::now().time_since_epoch()).count();
                                if (nowMs >= sentTs)
                                {
                                    g_Ping = (uint32_t)(nowMs - sentTs);
                                }
                            }
                        }
                        else if (hdr->opcode == (uint16_t)Opcode::ACK)
                        {
                            // Server ACK received; do not enqueue to Lua
                        }
                        else
                        {
                            // Enqueue for Lua
                            uint32_t head = g_RingHead.load(std::memory_order_relaxed);
                            uint32_t nextHead = (head + 1) % 256;
                            if (nextHead != g_RingTail.load(std::memory_order_acquire))
                            {
                                g_Ring[head].len = (size_t)res;
                                memcpy(g_Ring[head].data, recvBuf, res);
                                g_RingHead.store(nextHead, std::memory_order_release);
                            }
                            else
                            {
                                g_DroppedPackets.fetch_add(1, std::memory_order_relaxed);
                            }
                        }
                    }
                }
            }
        }
    }
}

namespace NetClient
{
    void Init()
    {
        if (g_Running || g_Socket != INVALID_SOCKET)
            return;

        WSADATA wsaData;
        int wsaRes = WSAStartup(MAKEWORD(2,2), &wsaData);
        if (wsaRes != 0)
        {
            std::lock_guard<std::mutex> lock(g_StateMutex);
            g_LastError = "WSAStartup failed: " + std::to_string(wsaRes);
            return;
        }
        
        g_Socket = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
        if (g_Socket == INVALID_SOCKET)
        {
            int err = WSAGetLastError();
            {
                std::lock_guard<std::mutex> lock(g_StateMutex);
                g_LastError = "Failed to create UDP socket: " + std::to_string(err);
            }
            WSACleanup();
            return;
        }
        
        u_long mode = 1;
        if (ioctlsocket(g_Socket, FIONBIO, &mode) != 0)
        {
            int err = WSAGetLastError();
            {
                std::lock_guard<std::mutex> lock(g_StateMutex);
                g_LastError = "ioctlsocket FIONBIO failed: " + std::to_string(err);
            }
            closesocket(g_Socket);
            g_Socket = INVALID_SOCKET;
            WSACleanup();
            return;
        }
        
        g_Running = true;
        g_NetThread = std::thread(BackgroundThread);
    }

    void Disconnect()
    {
        bool sendDisconnect = false;
        {
            std::lock_guard<std::mutex> lock(g_StateMutex);
            if (g_State == CONNECTED || g_State == CONNECTING)
            {
                sendDisconnect = true;
            }
            g_State = DISCONNECTED;
            g_SessionID = 0;
            g_SpawnX = 0.0f; g_SpawnY = 0.0f; g_SpawnZ = 0.0f;
            g_StartLevel = 0; g_EcoTierCache = 0;
            memset(g_Faction, 0, sizeof(g_Faction));
        }
        g_PendingCreate.store(false);
        g_HasCharacter.store(false);
        g_InSafeZone.store(false, std::memory_order_release);
        g_StateCV.notify_all();

        if (sendDisconnect)
        {
            ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::DISCONNECT, 0 };
            SendPacket((char*)&hdr, sizeof(hdr));
        }
    }

    void Shutdown()
    {
        Disconnect();

        {
            std::lock_guard<std::mutex> lock(g_StateMutex);
            g_Running = false;
        }
        g_StateCV.notify_all();
        if (g_NetThread.joinable())
            g_NetThread.join();
            
        if (g_Socket != INVALID_SOCKET)
        {
            closesocket(g_Socket);
            g_Socket = INVALID_SOCKET;
        }
        QueryCancelAll();
        WSACleanup();
    }

    void Connect(const std::string& ip, uint16_t port, const std::string& uuid, uint32_t hwid, const std::string& nick)
    {
        std::lock_guard<std::mutex> lock(g_StateMutex);
        g_TargetIP = ip;
        g_TargetPort = port;
        g_UUID = uuid;
        g_HWID = hwid;
        g_Nick = nick;
        
        memset(&g_ServerAddr, 0, sizeof(g_ServerAddr));
        g_ServerAddr.sin_family = AF_INET;
        g_ServerAddr.sin_port = htons(port);
        int ptonRes = inet_pton(AF_INET, ip.c_str(), &g_ServerAddr.sin_addr);
        if (ptonRes != 1)
        {
            g_LastError = "Invalid server IP address: " + ip;
            g_State = DISCONNECTED;
            g_StateCV.notify_all();
            return;
        }
        
        g_ConnectRetries = 0;
        g_ConnectBackoffMs = 1000;
        g_ConnectStartTime = std::chrono::steady_clock::now();
        g_LastConnectTime = g_ConnectStartTime - std::chrono::milliseconds(g_ConnectBackoffMs + 1);
        g_LastPacketReceivedTime = g_ConnectStartTime;
        g_SessionID = 0;
        g_PendingCreate.store(false);
        g_HasCharacter.store(false);
        g_SpawnX = 0.0f; g_SpawnY = 0.0f; g_SpawnZ = 0.0f;
        g_StartLevel = 0; g_EcoTierCache = 0;
        memset(g_Faction, 0, sizeof(g_Faction));
        g_InSafeZone.store(false, std::memory_order_release);
        g_LastError.clear();

        g_State = CONNECTING;
        g_StateCV.notify_all();
    }

    void SendTransform(const Transform& transform, uint16_t gvid)
    {
        std::lock_guard<std::mutex> lock(g_TransformMutex);
        g_Transform.x = transform.x; g_Transform.y = transform.y; g_Transform.z = transform.z;
        g_Transform.yaw = transform.yaw; g_Transform.pitch = transform.pitch;
        g_Transform.vx = transform.vx; g_Transform.vy = transform.vy; g_Transform.vz = transform.vz;
        g_Transform.animflags = transform.animflags;
        g_Transform.gvid = gvid;
        g_Transform.updated = true;
    }

    void SendLevelChange(const std::string& level)
    {
        if (g_State != CONNECTED) return;
        LevelChange lc = {};
        size_t n = std::min(level.length(), sizeof(lc.level) - 1);
        memcpy(lc.level, level.c_str(), n);
        ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::LEVEL_CHANGE, sizeof(lc) };
        char buf[sizeof(hdr) + sizeof(lc)];
        memcpy(buf, &hdr, sizeof(hdr));
        memcpy(buf + sizeof(hdr), &lc, sizeof(lc));
        SendPacket(buf, sizeof(buf));
    }

    void SendPlayerVisual(const std::string& visual)
    {
        if (g_State != CONNECTED) return;
        PlayerVisual pv = {};
        size_t n = std::min(visual.length(), sizeof(pv.visual) - 1);
        memcpy(pv.visual, visual.c_str(), n);
        ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::PLAYER_VISUAL, sizeof(pv) };
        char buf[sizeof(hdr) + sizeof(pv)];
        memcpy(buf, &hdr, sizeof(hdr));
        memcpy(buf + sizeof(hdr), &pv, sizeof(pv));
        SendPacket(buf, sizeof(buf));
    }

    bool PollEvent(char* outBuf, size_t maxLen, size_t& outLen)
    {
        uint32_t tail = g_RingTail.load(std::memory_order_relaxed);
        if (tail == g_RingHead.load(std::memory_order_acquire))
            return false;
            
        size_t packetLen = g_Ring[tail].len;
        if (packetLen > maxLen)
        {
            g_DroppedPackets.fetch_add(1, std::memory_order_relaxed);
            g_RingTail.store((tail + 1) % 256, std::memory_order_release);
            return false;
        }

        outLen = packetLen;
        memcpy(outBuf, g_Ring[tail].data, outLen);
        
        g_RingTail.store((tail + 1) % 256, std::memory_order_release);
        return true;
    }

    bool PollEvent(char* outBuf, size_t& outLen)
    {
        // Legacy 2-arg overload: capacity is implicitly 1500 (the FFI
        // g_poll_buf size). The Lua side never presets *len (it holds the
        // previous packet's length), so reading it as a capacity would cause
        // false drops. New native code must call the 3-arg overload with an
        // explicit capacity. ZN_PollEvent below does exactly that.
        return PollEvent(outBuf, 1500, outLen);
    }

    bool PollEvent(int a, int& b)
    {
        uint32_t tail = g_RingTail.load(std::memory_order_relaxed);
        if (tail == g_RingHead.load(std::memory_order_acquire))
            return false;

        size_t packetLen = g_Ring[tail].len;
        if (packetLen >= sizeof(ZO_Header))
        {
            ZO_Header* hdr = (ZO_Header*)g_Ring[tail].data;
            a = (int)hdr->opcode;
            b = (int)hdr->payload_len;
        }
        else
        {
            a = 0;
            b = (int)packetLen;
        }

        g_RingTail.store((tail + 1) % 256, std::memory_order_release);
        return true;
    }
    
    bool IsConnected() { return g_State == CONNECTED; }
    bool IsInSafeZone() { return g_InSafeZone.load(std::memory_order_acquire); }
    bool IsPendingCreate() { return g_PendingCreate.load(std::memory_order_acquire); }
    bool HasCharacter() { return g_HasCharacter.load(std::memory_order_acquire); }
    NetClient::Spawn GetSpawn()
    {
        std::lock_guard<std::mutex> lock(g_StateMutex);
        NetClient::Spawn s;
        s.x = g_SpawnX; s.y = g_SpawnY; s.z = g_SpawnZ;
        s.level = g_StartLevel; s.ecoTier = g_EcoTierCache;
        return s;
    }
    std::string GetFaction()
    {
        std::lock_guard<std::mutex> lock(g_StateMutex);
        size_t n = 0;
        while (n < sizeof(g_Faction) && g_Faction[n] != '\0') n++;
        return std::string(g_Faction, n);
    }
    
    void GetSessionInfo(uint32_t& outSessionID, uint32_t& outPing)
    {
        outSessionID = g_SessionID.load(std::memory_order_relaxed);
        outPing = g_Ping.load(std::memory_order_relaxed);
    }
    
    std::string GetLastError()
    {
        std::lock_guard<std::mutex> lock(g_StateMutex);
        return g_LastError;
    }

    uint64_t GetDroppedPackets()
    {
        return g_DroppedPackets.load(std::memory_order_relaxed);
    }
    
    void SendChatText(const std::string& text)
    {
        if (g_State != CONNECTED) return;
        ChatText ct = {};
        ct.senderID = g_SessionID.load(std::memory_order_relaxed);
        ct.len = (uint8_t)std::min(text.length(), sizeof(ct.text) - 1);
        snprintf(ct.text, sizeof(ct.text), "%s", text.c_str());
        
        ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::CHAT_TEXT, sizeof(ct) };
        char buf[1500];
        memcpy(buf, &hdr, sizeof(hdr));
        memcpy(buf + sizeof(hdr), &ct, sizeof(ct));
        SendPacket(buf, sizeof(hdr) + sizeof(ct));
    }

    bool SendCharacterSelect(const std::string& faction, uint32_t money, const std::string& items)
    {
        if (g_State != CONNECTED) return false;
        CharacterSelect cs = {};
        size_t flen = std::min(faction.length(), sizeof(cs.faction) - 1);
        memcpy(cs.faction, faction.c_str(), flen);
        cs.money = money;
        size_t ilen = std::min(items.length(), sizeof(cs.items) - 1);
        memcpy(cs.items, items.c_str(), ilen);
        ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::CHARACTER_SELECT, sizeof(cs) };
        char buf[sizeof(hdr) + sizeof(cs)];
        memcpy(buf, &hdr, sizeof(hdr));
        memcpy(buf + sizeof(hdr), &cs, sizeof(cs));
        SendPacket(buf, sizeof(buf));
        return true;
    }

    int SendRaw(uint16_t opcode, const char* payload, int payloadLen, bool reliable)
    {
        if (opcode == 0 || payloadLen < 0 || payloadLen > kMaxRawPayload)
            return -1;
        if (payloadLen > 0 && !payload)
            return -1;
        if (g_State != CONNECTED)
            return -2;

        ZO_Header hdr = { 0x5A4F, 1,
                          reliable ? FlagReliable : FlagUnreliable,
                          ++g_Sequence, opcode, (uint16_t)payloadLen };
        char buf[sizeof(ZO_Header) + kMaxRawPayload];
        memcpy(buf, &hdr, sizeof(hdr));
        if (payloadLen > 0)
        {
            memcpy(buf + sizeof(hdr), payload, (size_t)payloadLen);
        }
        SendPacket(buf, (int)sizeof(hdr) + payloadLen);

        // NOTE: reliability here only marks the packet so the server ACKs the
        // inbound datagram. The client has no unacked-raw table and performs no
        // retransmit for ZN_SendRaw; outbound loss recovery for raw opcodes is
        // a documented limitation, not a feature of this send path.
        return 0;
    }

    int QueryServer(const std::string& ip, uint16_t port, char* outBuf, int outBufLen, int timeoutMs)
    {
        if (ip.empty() || !outBuf || outBufLen <= 0 || port == 0)
            return -1;

        // Dedicated temporary socket + local sequence: never touches the
        // session socket, the send mutex, or the event ring, so this is safe
        // to call from any thread while the net thread is running.
        WSADATA wsaData;
        if (WSAStartup(MAKEWORD(2, 2), &wsaData) != 0)
            return -1;

        SOCKET sock = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
        if (sock == INVALID_SOCKET)
        {
            WSACleanup();
            return -1;
        }

        int result = -1;
        sockaddr_in target = {};
        target.sin_family = AF_INET;
        target.sin_port = htons(port);
        if (inet_pton(AF_INET, ip.c_str(), &target.sin_addr) != 1)
        {
            closesocket(sock);
            WSACleanup();
            return result;
        }

        uint32_t seq = 1;
        ZO_Header hdr = { 0x5A4F, 1, 1, seq, (uint16_t)Opcode::SERVER_QUERY, 0 };
        int sent = sendto(sock, (const char*)&hdr, sizeof(hdr), 0, (sockaddr*)&target, sizeof(target));
        if (sent == SOCKET_ERROR)
        {
            closesocket(sock);
            WSACleanup();
            return -2;
        }

        result = -3;
        auto deadline = std::chrono::steady_clock::now() +
            std::chrono::milliseconds(timeoutMs > 0 ? timeoutMs : 0);
        for (;;)
        {
            auto now = std::chrono::steady_clock::now();
            long long remainingMs =
                std::chrono::duration_cast<std::chrono::milliseconds>(deadline - now).count();
            if (remainingMs < 0) remainingMs = 0;

            fd_set readfds;
            FD_ZERO(&readfds);
            FD_SET(sock, &readfds);
            timeval tv;
            tv.tv_sec = (long)(remainingMs / 1000);
            tv.tv_usec = (long)((remainingMs % 1000) * 1000);
            int sel = select(0, &readfds, nullptr, nullptr, &tv);
            if (sel <= 0)
                break;

            char recvBuf[1500];
            sockaddr_in from;
            int fromLen = sizeof(from);
            int res = recvfrom(sock, recvBuf, sizeof(recvBuf), 0, (sockaddr*)&from, &fromLen);
            if (res < (int)sizeof(ZO_Header))
                continue;

            ZO_Header* rhdr = (ZO_Header*)recvBuf;
            if (rhdr->magic != 0x5A4F || rhdr->opcode != (uint16_t)Opcode::SERVER_QUERY_RES)
                continue;

            int payloadLen = res - (int)sizeof(ZO_Header);
            if (payloadLen > outBufLen)
            {
                result = -4;
                break;
            }
            memcpy(outBuf, recvBuf + sizeof(ZO_Header), payloadLen);
            result = payloadLen;
            break;
        }

        closesocket(sock);
        WSACleanup();
        return result;
    }

    int QueryStart(const std::string& ip, uint16_t port, int timeoutMs)
    {
        if (ip.empty() || port == 0 || timeoutMs <= 0)
            return -1;

        sockaddr_in target = {};
        target.sin_family = AF_INET;
        target.sin_port = htons(port);
        if (inet_pton(AF_INET, ip.c_str(), &target.sin_addr) != 1)
            return -1;

        // One WSAStartup reference per in-flight query; released in
        // CloseQuerySocket when the entry leaves g_PendingQueries.
        WSADATA wsaData;
        if (WSAStartup(MAKEWORD(2, 2), &wsaData) != 0)
            return -1;

        SOCKET sock = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
        if (sock == INVALID_SOCKET)
        {
            WSACleanup();
            return -1;
        }

        u_long nonBlocking = 1;
        if (ioctlsocket(sock, FIONBIO, &nonBlocking) != 0)
        {
            closesocket(sock);
            WSACleanup();
            return -1;
        }

        auto rttStart = std::chrono::steady_clock::now();
        uint32_t seq = g_QuerySequence.fetch_add(1, std::memory_order_relaxed);
        ZO_Header hdr = { 0x5A4F, 1, 1, seq, (uint16_t)Opcode::SERVER_QUERY, 0 };
        int sent = sendto(sock, (const char*)&hdr, sizeof(hdr), 0, (sockaddr*)&target, sizeof(target));
        if (sent == SOCKET_ERROR)
        {
            closesocket(sock);
            WSACleanup();
            return -1;
        }

        std::lock_guard<std::mutex> lock(g_QueryMutex);
        SweepExpiredQueries(rttStart);

        if (g_PendingQueries.size() >= kMaxPendingQueries)
        {
            closesocket(sock);
            WSACleanup();
            return -2;
        }

        int queryId = 0;
        do
        {
            queryId = g_NextQueryId.fetch_add(1, std::memory_order_relaxed);
        } while (queryId <= 0);

        PendingQuery query;
        query.id = queryId;
        query.sock = sock;
        query.deadline = rttStart + std::chrono::milliseconds(timeoutMs);
        query.rttStart = rttStart;
        g_PendingQueries.push_back(query);
        return queryId;
    }

    int QueryPoll(int queryId, char* outBuf, int outBufLen, int* outRttMs)
    {
        if (queryId <= 0 || !outBuf || outBufLen <= 0)
            return -1;

        std::lock_guard<std::mutex> lock(g_QueryMutex);

        auto it = std::find_if(g_PendingQueries.begin(), g_PendingQueries.end(),
            [queryId](const PendingQuery& q) { return q.id == queryId; });
        if (it == g_PendingQueries.end())
            return -1;

        auto now = std::chrono::steady_clock::now();
        if (now >= it->deadline)
        {
            CloseQuerySocket(*it);
            g_PendingQueries.erase(it);
            return -1;
        }

        // Bounded, zero-timeout receive loop on this query's socket only.
        int result = 0;
        char recvBuf[1500];
        for (int iter = 0; iter < 32; ++iter)
        {
            fd_set readfds;
            FD_ZERO(&readfds);
            FD_SET(it->sock, &readfds);
            timeval tv = {0, 0};
            int sel = select(0, &readfds, nullptr, nullptr, &tv);
            if (sel <= 0)
                break;

            sockaddr_in from;
            int fromLen = sizeof(from);
            int res = recvfrom(it->sock, recvBuf, sizeof(recvBuf), 0, (sockaddr*)&from, &fromLen);
            if (res == SOCKET_ERROR)
            {
                int err = WSAGetLastError();
                if (err == WSAEWOULDBLOCK)
                    break;
                // WSAECONNRESET (ICMP port unreachable) and other receive
                // errors are terminal for this query.
                result = -1;
                break;
            }
            if (res < (int)sizeof(ZO_Header))
                continue;

            ZO_Header* rhdr = (ZO_Header*)recvBuf;
            if (rhdr->magic != 0x5A4F || rhdr->opcode != (uint16_t)Opcode::SERVER_QUERY_RES)
                continue;

            int payloadLen = res - (int)sizeof(ZO_Header);
            if (payloadLen > outBufLen)
            {
                result = -1;
                break;
            }

            memcpy(outBuf, recvBuf + sizeof(ZO_Header), payloadLen);
            if (outRttMs)
            {
                auto rtt = std::chrono::duration_cast<std::chrono::milliseconds>(
                    std::chrono::steady_clock::now() - it->rttStart).count();
                *outRttMs = (int)rtt;
            }
            result = payloadLen;
            break;
        }

        if (result == 0)
        {
            now = std::chrono::steady_clock::now();
            if (now >= it->deadline)
            {
                CloseQuerySocket(*it);
                g_PendingQueries.erase(it);
                return -1;
            }
            return 0;
        }

        CloseQuerySocket(*it);
        g_PendingQueries.erase(it);
        return result;
    }

    void QueryCancel(int queryId)
    {
        if (queryId <= 0)
            return;

        std::lock_guard<std::mutex> lock(g_QueryMutex);
        auto it = std::find_if(g_PendingQueries.begin(), g_PendingQueries.end(),
            [queryId](const PendingQuery& q) { return q.id == queryId; });
        if (it == g_PendingQueries.end())
            return;

        CloseQuerySocket(*it);
        g_PendingQueries.erase(it);
    }

    void QueryCancelAll()
    {
        std::lock_guard<std::mutex> lock(g_QueryMutex);
        for (auto& query : g_PendingQueries)
        {
            CloseQuerySocket(query);
        }
        g_PendingQueries.clear();
    }
}
