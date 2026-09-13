#include "udp_client.h"
#include "../protocol/packets.h"
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
    sockaddr_in g_ServerAddr;
    
    enum State { DISCONNECTED, CONNECTING, CONNECTED };
    std::atomic<State> g_State = DISCONNECTED;
    
    uint32_t g_SessionID = 0;
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
    
    std::mutex g_StateMutex;
    std::condition_variable g_StateCV;
    
    std::string g_TargetIP;
    uint16_t g_TargetPort;
    std::string g_UUID;
    uint32_t g_HWID;
    std::string g_Nick;
    
    uint32_t g_Sequence = 0;
    
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
        int res = sendto(g_Socket, buf, len, 0, (sockaddr*)&g_ServerAddr, sizeof(g_ServerAddr));
        if (res == SOCKET_ERROR)
        {
            int err = WSAGetLastError();
            // WSAEWOULDBLOCK is normal for non-blocking sockets.
            if (err != WSAEWOULDBLOCK)
            {
                g_LastError = "sendto failed: " + std::to_string(err);
                if (err == WSAECONNRESET) // ICMP port unreachable
                {
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
        auto lastConnect = steady_clock::now();
        int backoff = 3000;
        
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
                if (duration_cast<milliseconds>(now - lastConnect).count() > backoff)
                {
                    // Send Handshake
                    HandshakeReq req = {};
                    snprintf(req.uuid, sizeof(req.uuid), "%s", g_UUID.c_str());
                    req.hwid = g_HWID;
                    snprintf(req.nick, sizeof(req.nick), "%s", g_Nick.c_str());
                    req.protoVer = 1;
                    
                    ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::HANDSHAKE_REQ, sizeof(req) };
                    char buf[1500];
                    memcpy(buf, &hdr, sizeof(hdr));
                    memcpy(buf + sizeof(hdr), &req, sizeof(req));
                    
                    SendPacket(buf, sizeof(hdr) + sizeof(req));
                    
                    lastConnect = now;
                    backoff = std::min(backoff * 2, 30000);
                }
            }
            else if (g_State == CONNECTED)
            {
                if (duration_cast<milliseconds>(now - lastHeartbeat).count() > 1000)
                {
                    ZO_Header hdr = { 0x5A4F, 1, 0, ++g_Sequence, (uint16_t)Opcode::HEARTBEAT, 0 };
                    SendPacket((char*)&hdr, sizeof(hdr));
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
                if (res >= sizeof(ZO_Header))
                {
                    ZO_Header* hdr = (ZO_Header*)recvBuf;
                    if (hdr->magic == 0x5A4F)
                    {
                        if (hdr->opcode == (uint16_t)Opcode::HANDSHAKE_RES)
                        {
                            if (res >= sizeof(ZO_Header) + sizeof(HandshakeRes))
                            {
                                HandshakeRes* hr = (HandshakeRes*)(recvBuf + sizeof(ZO_Header));
                                g_SessionID = hr->sessionID;
                                g_State = CONNECTED;
                            }
                        }
                        else if (hdr->opcode == (uint16_t)Opcode::SAFEZONE_STATE)
                        {
                            if (res >= sizeof(ZO_Header) + sizeof(SafeZoneState))
                            {
                                SafeZoneState* sz = (SafeZoneState*)(recvBuf + sizeof(ZO_Header));
                                g_InSafeZone = (sz->locked != 0);
                            }
                            
                            // Enqueue for Lua so zone_safezone.script receives it
                            uint32_t head = g_RingHead.load(std::memory_order_relaxed);
                            uint32_t nextHead = (head + 1) % 256;
                            if (nextHead != g_RingTail.load(std::memory_order_acquire))
                            {
                                g_Ring[head].len = res;
                                memcpy(g_Ring[head].data, recvBuf, res);
                                g_RingHead.store(nextHead, std::memory_order_release);
                            }
                        }
                        else
                        {
                            // Enqueue for Lua
                            uint32_t head = g_RingHead.load(std::memory_order_relaxed);
                            uint32_t nextHead = (head + 1) % 256;
                            if (nextHead != g_RingTail.load(std::memory_order_acquire))
                            {
                                g_Ring[head].len = res;
                                memcpy(g_Ring[head].data, recvBuf, res);
                                g_RingHead.store(nextHead, std::memory_order_release);
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
        WSADATA wsaData;
        WSAStartup(MAKEWORD(2,2), &wsaData);
        
        g_Socket = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
        
        u_long mode = 1;
        ioctlsocket(g_Socket, FIONBIO, &mode);
        
        g_Running = true;
        g_NetThread = std::thread(BackgroundThread);
    }

    void Shutdown()
    {
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
        
        g_ServerAddr.sin_family = AF_INET;
        g_ServerAddr.sin_port = htons(port);
        inet_pton(AF_INET, ip.c_str(), &g_ServerAddr.sin_addr);
        
        g_State = CONNECTING;
        g_StateCV.notify_all();
    }

    void Disconnect()
    {
        std::lock_guard<std::mutex> lock(g_StateMutex);
        if (g_State == CONNECTED)
        {
            ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::DISCONNECT, 0 };
            SendPacket((char*)&hdr, sizeof(hdr));
        }
        g_State = DISCONNECTED;
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

    bool PollEvent(char* outBuf, size_t& outLen)
    {
        uint32_t tail = g_RingTail.load(std::memory_order_relaxed);
        if (tail == g_RingHead.load(std::memory_order_acquire))
            return false;
            
        outLen = g_Ring[tail].len;
        memcpy(outBuf, g_Ring[tail].data, outLen);
        
        g_RingTail.store((tail + 1) % 256, std::memory_order_release);
        return true;
    }
    
    bool IsConnected() { return g_State == CONNECTED; }
    bool IsInSafeZone() { return g_InSafeZone; }
    
    void GetSessionInfo(uint32_t& outSessionID, uint32_t& outPing)
    {
        outSessionID = g_SessionID;
        outPing = g_Ping;
    }
    
    std::string GetLastError() { return g_LastError; }
    
    void SendChatText(const std::string& text)
    {
        if (g_State != CONNECTED) return;
        ChatText ct = {};
        ct.senderID = g_SessionID;
        ct.len = (uint8_t)std::min(text.length(), sizeof(ct.text) - 1);
        snprintf(ct.text, sizeof(ct.text), "%s", text.c_str());
        
        ZO_Header hdr = { 0x5A4F, 1, 1, ++g_Sequence, (uint16_t)Opcode::CHAT_TEXT, sizeof(ct) };
        char buf[1500];
        memcpy(buf, &hdr, sizeof(hdr));
        memcpy(buf + sizeof(hdr), &ct, sizeof(ct));
        SendPacket(buf, sizeof(hdr) + sizeof(ct));
    }
}
