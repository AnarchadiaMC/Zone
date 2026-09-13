#include "identity.h"
#include <windows.h>
#include <shlobj.h>
#include <fstream>
#include <sstream>
#include <Rpc.h>
#pragma comment(lib, "Rpcrt4.lib")

namespace
{
    std::string g_UUID;
    uint32_t g_HWID = 0;
    std::string g_Nickname = "Stalker";
    std::string g_ConfigPath;

    std::string GenerateUUID()
    {
        UUID uuid;
        UuidCreate(&uuid);
        RPC_CSTR str;
        UuidToStringA(&uuid, &str);
        std::string res((char*)str);
        RpcStringFreeA(&str);
        return res;
    }

    uint32_t FNV1a(const std::string& str)
    {
        uint32_t hash = 2166136261u;
        for (char c : str)
        {
            hash ^= (uint8_t)c;
            hash *= 16777619u;
        }
        return hash;
    }

    uint32_t GenerateHWID()
    {
        char value[256] = {0};
        DWORD size = sizeof(value);
        HKEY hKey;
        if (RegOpenKeyExA(HKEY_LOCAL_MACHINE, "SOFTWARE\\Microsoft\\Cryptography", 0, KEY_READ | KEY_WOW64_64KEY, &hKey) == ERROR_SUCCESS)
        {
            RegQueryValueExA(hKey, "MachineGuid", nullptr, nullptr, (LPBYTE)value, &size);
            RegCloseKey(hKey);
        }

        char compName[256] = {0};
        DWORD compSize = sizeof(compName);
        GetComputerNameA(compName, &compSize);

        std::string combined = std::string(value) + compName;
        return FNV1a(combined);
    }
}

namespace Identity
{
    void Init()
    {
        char path[MAX_PATH];
        if (SUCCEEDED(SHGetFolderPathA(NULL, CSIDL_APPDATA, NULL, 0, path)))
        {
            g_ConfigPath = std::string(path) + "\\zone_identity.ltx";
            
            std::ifstream in(g_ConfigPath);
            if (in.is_open())
            {
                std::string line;
                while (std::getline(in, line))
                {
                    if (line.find("uuid=") == 0) g_UUID = line.substr(5);
                    else if (line.find("hwid=") == 0) g_HWID = std::stoul(line.substr(5));
                    else if (line.find("nickname=") == 0) g_Nickname = line.substr(9);
                }
            }
        }

        bool needSave = false;
        if (g_UUID.empty())
        {
            g_UUID = GenerateUUID();
            needSave = true;
        }
        if (g_HWID == 0)
        {
            g_HWID = GenerateHWID();
            needSave = true;
        }

        if (needSave)
            Save();
    }

    std::string GetClientUUID() { return g_UUID; }
    uint32_t GetHWIDHash() { return g_HWID; }
    std::string GetNickname() { return g_Nickname; }

    void SetNickname(const std::string& nick)
    {
        g_Nickname = nick;
        Save();
    }

    void Save()
    {
        if (g_ConfigPath.empty()) return;
        std::ofstream out(g_ConfigPath);
        if (out.is_open())
        {
            out << "[identity]\n";
            out << "uuid=" << g_UUID << "\n";
            out << "hwid=" << g_HWID << "\n";
            out << "nickname=" << g_Nickname << "\n";
        }
    }
}
