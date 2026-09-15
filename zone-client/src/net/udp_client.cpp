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
        bool updated = false;
    } g_Transform;
    std::mutex g_TransformMutex;

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
                    }
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
                            if (res >= (int)(sizeof(ZO_Header) + sizeof(HandshakeRes)))
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
                                    g_State = DISCONNECTED;
                                    g_StateCV.notify_all();
                                }
                                else
                                {
                                    g_SessionID = hr->sessionID;
                                    {
                                        std::lock_guard<std::mutex> lock(g_StateMutex);
                                        g_State = CONNECTED;
                                    }
                                    g_StateCV.notify_all();
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
                                g_State = DISCONNECTED;
                            }
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
        }
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
        g_InSafeZone.store(false, std::memory_order_release);
        g_LastError.clear();

        g_State = CONNECTING;
        g_StateCV.notify_all();
    }

    void SendTransform(const Transform& transform)
    {
        std::lock_guard<std::mutex> lock(g_TransformMutex);
        g_Transform.x = transform.x; g_Transform.y = transform.y; g_Transform.z = transform.z;
        g_Transform.yaw = transform.yaw; g_Transform.pitch = transform.pitch;
        g_Transform.vx = transform.vx; g_Transform.vy = transform.vy; g_Transform.vz = transform.vz;
        g_Transform.animflags = transform.animflags;
        g_Transform.updated = true;
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
}
