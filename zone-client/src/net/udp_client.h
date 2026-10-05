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

    // Generic escape hatch for new client->server opcodes (item actions, fire
    // events, future features) without a typed helper per feature. Builds a
    // ZO_Header with the given opcode/payload/flags and sends it on the
    // existing session socket. The sequence comes from the same atomic counter
    // as every other send, and the socket write goes through the shared
    // g_SendMutex path, so this is safe to call from any thread and never
    // blocks beyond a normal non-blocking send.
    //
    // reliable != 0 sets flag 0x01 (reliable), otherwise flag 0x02
    // (unreliable); see ZO_Header in protocol/packets.h.
    //
    // Returns 0 on success; -1 invalid argument (opcode 0, null payload with
    // payloadLen > 0, or payloadLen outside [0, 1200]); -2 when not connected.
    //
    // LIMITATION: reliable outbound raw packets are marked and the server ACKs
    // them inbound, but the client keeps no unacked-packet table and does not
    // retransmit. Loss recovery for reliable raw sends is not implemented.
    int SendRaw(uint16_t opcode, const char* payload, int payloadLen, bool reliable);

    // One-shot server query on a dedicated temporary UDP socket. Does not touch
    // the session socket or the event ring. Sends OpServerQuery and waits up to
    // timeoutMs for OpServerQueryRes. Copies the raw payload into outBuf and
    // returns the payload byte count, or a negative error:
    //   -1 invalid argument / WSA startup / socket failure
    //   -2 sendto failed
    //   -3 timed out waiting for a matching response
    //   -4 response payload larger than outBufLen
    // WARNING: blocks the calling thread until the response arrives or
    // timeoutMs elapses. Prefer QueryStart/QueryPoll from the game UI thread.
    int QueryServer(const std::string& ip, uint16_t port, char* outBuf, int outBufLen, int timeoutMs);

    // Non-blocking server query. QueryStart sends OpServerQuery on a dedicated
    // temporary UDP socket and returns a positive query id, or a negative error:
    //   -1 invalid argument / WSA startup / socket failure / sendto failure
    //   -2 too many queries in flight (cap is 32)
    // The query table is mutex-guarded and internally capped/expired; these
    // functions never block on the network and are safe to call from any thread
    // or Lua VM thread.
    int QueryStart(const std::string& ip, uint16_t port, int timeoutMs);

    // Polls one in-flight query with bounded, zero-timeout work:
    //   > 0 = response ready; payload copied to outBuf, return = payload bytes,
    //         elapsed RTT (ms) written to outRttMs when non-null
    //   0   = still pending (entry kept, hard-expires at its deadline)
    //   -1  = failed / timed out / unknown id / response larger than outBufLen
    //         (entry is cleaned up in every -1 case)
    int QueryPoll(int queryId, char* outBuf, int outBufLen, int* outRttMs);

    // Closes the query's socket and drops the entry. Idempotent; unknown ids are
    // ignored. Safe to call concurrently with QueryPoll/QueryStart.
    void QueryCancel(int queryId);

    // Cancels every in-flight query.
    void QueryCancelAll();
}
