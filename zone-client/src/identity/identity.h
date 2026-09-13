#pragma once
#include <string>
#include <cstdint>

namespace Identity
{
    void Init();
    std::string GetClientUUID();
    uint32_t GetHWIDHash();
    std::string GetNickname();
    void SetNickname(const std::string& nick);
    void Save();
}
