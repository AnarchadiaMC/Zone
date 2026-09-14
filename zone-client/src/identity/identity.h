#pragma once
#include <string>
#include <cstdint>

namespace Identity
{
    void Init();
    std::string GetClientUUID();
    const uint8_t* GetHWIDBinary();
    const uint8_t* GetHWID();
    const char* GetHWIDString();
    uint32_t GetHWIDHash();
    std::string GetNickname();
    void SetNickname(const std::string& nick);
    void Save();
}
