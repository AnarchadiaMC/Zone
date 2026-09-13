#include "identity.h"
#include <windows.h>
#include <shlobj.h>
#include <fstream>
#include <sstream>
#include <rpc.h>
#pragma comment(lib, "Rpcrt4.lib")
#include "../provision/asset_provisioner.h"

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
        std::wstring machineGuid;
        HKEY hKey = nullptr;
        LSTATUS regStatus = RegOpenKeyExW(HKEY_LOCAL_MACHINE, L"SOFTWARE\\Microsoft\\Cryptography", 0, KEY_READ | KEY_WOW64_64KEY, &hKey);
        if (regStatus == ERROR_SUCCESS && hKey != nullptr)
        {
            wchar_t value[256] = {0};
            DWORD size = sizeof(value);
            DWORD type = 0;
            LSTATUS queryStatus = RegQueryValueExW(hKey, L"MachineGuid", nullptr, &type, reinterpret_cast<LPBYTE>(value), &size);
            if (queryStatus == ERROR_SUCCESS && (type == REG_SZ || type == REG_MULTI_SZ))
            {
                machineGuid = value;
            }
            RegCloseKey(hKey);
        }

        std::wstring compName;
        wchar_t compBuf[MAX_COMPUTERNAME_LENGTH + 1] = {0};
        DWORD compSize = MAX_COMPUTERNAME_LENGTH + 1;
        if (GetComputerNameW(compBuf, &compSize) && compSize > 0)
        {
            compName = compBuf;
        }

        // If reading MachineGuid or computer name fails, generate a fallback HWID
        if (machineGuid.empty() || compName.empty())
        {
            DWORD volumeSerial = 0;
            BOOL volOk = GetVolumeInformationW(L"C:\\", nullptr, 0, &volumeSerial, nullptr, nullptr, nullptr, 0);
            if (!volOk || volumeSerial == 0)
            {
                volOk = GetVolumeInformationW(nullptr, nullptr, 0, &volumeSerial, nullptr, nullptr, nullptr, 0);
            }

            if (volOk && volumeSerial != 0)
            {
                std::string fallback = "VOL_" + std::to_string(volumeSerial);
                if (!compName.empty())
                {
                    char compA[MAX_COMPUTERNAME_LENGTH + 1] = {0};
                    WideCharToMultiByte(CP_UTF8, 0, compName.c_str(), -1, compA, sizeof(compA), nullptr, nullptr);
                    fallback += "_";
                    fallback += compA;
                }
                if (!machineGuid.empty())
                {
                    char guidA[256] = {0};
                    WideCharToMultiByte(CP_UTF8, 0, machineGuid.c_str(), -1, guidA, sizeof(guidA), nullptr, nullptr);
                    fallback += "_";
                    fallback += guidA;
                }
                uint32_t hash = FNV1a(fallback);
                return (hash != 0) ? hash : 1;
            }

            // Pseudo-random UUID fallback
            std::string fallbackUUID = GenerateUUID();
            uint32_t hash = FNV1a(fallbackUUID);
            return (hash != 0) ? hash : 1;
        }

        char guidA[256] = {0};
        char compA[MAX_COMPUTERNAME_LENGTH + 1] = {0};
        WideCharToMultiByte(CP_UTF8, 0, machineGuid.c_str(), -1, guidA, sizeof(guidA), nullptr, nullptr);
        WideCharToMultiByte(CP_UTF8, 0, compName.c_str(), -1, compA, sizeof(compA), nullptr, nullptr);

        std::string combined = std::string(guidA) + compA;
        uint32_t hash = FNV1a(combined);
        return (hash != 0) ? hash : 1;
    }
}

namespace Identity
{
    void Init()
    {
        std::wstring root = AssetProvisioner::GetGameRoot();
        if (!root.empty())
        {
            std::wstring appdataPath = root + L"\\appdata";
            CreateDirectoryW(appdataPath.c_str(), nullptr);
            
            char pathA[MAX_PATH] = {0};
            int convResult = WideCharToMultiByte(CP_UTF8, 0, (appdataPath + L"\\zone_identity.ltx").c_str(), -1, pathA, MAX_PATH, nullptr, nullptr);
            if (convResult > 0)
            {
                g_ConfigPath = pathA;
            }
            else
            {
                CreateDirectoryA("appdata", nullptr);
                g_ConfigPath = "appdata/zone_identity.ltx";
            }
        }
        else
        {
            CreateDirectoryA("appdata", nullptr);
            g_ConfigPath = "appdata/zone_identity.ltx";
        }

        std::ifstream in(g_ConfigPath);
        if (in.is_open())
        {
            std::string line;
            while (std::getline(in, line))
            {
                if (line.find("uuid=") == 0) g_UUID = line.substr(5);
                else if (line.find("hwid=") == 0)
                {
                    try { g_HWID = std::stoul(line.substr(5)); }
                    catch (...) { g_HWID = 0; }
                }
                else if (line.find("nickname=") == 0) g_Nickname = line.substr(9);
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
