#include "identity.h"
#include <windows.h>
#include <wincrypt.h>
#include <shlobj.h>
#include <rpc.h>
#include <cctype>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <mutex>
#include <string>
#include <vector>
#pragma comment(lib, "Rpcrt4.lib")
#pragma comment(lib, "Advapi32.lib")
#include "../provision/asset_provisioner.h"

namespace
{
    std::string g_UUID;
    uint8_t g_HWIDBinary[32] = {0};
    char g_HWIDString[65] = {0};
    uint32_t g_HWID = 0;
    std::string g_HWIDHashString;
    std::string g_Nickname = "Stalker";
    std::wstring g_ConfigPathW;
    std::mutex g_IdentityMutex;

    // Canonical identity file schema, shared with zone_main.script and the
    // server-browser favorites writer:
    //
    //   [zone_identity]
    //   client_uuid = <rfc4122 uuid>
    //   hwid_hash   = <uint32, decimal>
    //   nickname    = <callsign>
    //
    // Legacy keys uuid= / hwid= are accepted as read-only fallbacks and are
    // never rewritten. The same file carries [zone_favorites], [zone_history]
    // and [server_history] entries written by the Lua server browser, so every
    // write merges ONLY the three canonical keys and preserves every other
    // line/section byte-for-byte. Writes are atomic (temp + MoveFileExW) and
    // never truncate the original file.

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

    bool ComputeSHA256(const std::string& input, uint8_t outBinary[32])
    {
        HCRYPTPROV hProv = 0;
        if (!CryptAcquireContextW(&hProv, nullptr, MS_ENH_RSA_AES_PROV, PROV_RSA_AES, CRYPT_VERIFYCONTEXT))
        {
            if (!CryptAcquireContextW(&hProv, nullptr, nullptr, PROV_RSA_AES, CRYPT_VERIFYCONTEXT))
            {
                return false;
            }
        }

        HCRYPTHASH hHash = 0;
        bool success = false;
        if (CryptCreateHash(hProv, CALG_SHA_256, 0, 0, &hHash))
        {
            if (CryptHashData(hHash, reinterpret_cast<const BYTE*>(input.data()), static_cast<DWORD>(input.size()), 0))
            {
                DWORD hashLen = 32;
                if (CryptGetHashParam(hHash, HP_HASHVAL, outBinary, &hashLen, 0) && hashLen == 32)
                {
                    success = true;
                }
            }
            CryptDestroyHash(hHash);
        }
        CryptReleaseContext(hProv, 0);
        return success;
    }

    void GenerateHWID()
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

        std::string identStr;

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
                identStr = fallback;
            }
            else
            {
                identStr = GenerateUUID();
            }
        }
        else
        {
            char guidA[256] = {0};
            char compA[MAX_COMPUTERNAME_LENGTH + 1] = {0};
            WideCharToMultiByte(CP_UTF8, 0, machineGuid.c_str(), -1, guidA, sizeof(guidA), nullptr, nullptr);
            WideCharToMultiByte(CP_UTF8, 0, compName.c_str(), -1, compA, sizeof(compA), nullptr, nullptr);

            identStr = std::string(guidA) + compA;
        }

        memset(g_HWIDBinary, 0, sizeof(g_HWIDBinary));
        if (!ComputeSHA256(identStr, g_HWIDBinary))
        {
            uint32_t f = FNV1a(identStr);
            memcpy(g_HWIDBinary, &f, sizeof(f));
        }

        for (size_t i = 0; i < 32; ++i)
        {
            snprintf(&g_HWIDString[i * 2], 3, "%02x", g_HWIDBinary[i]);
        }
        g_HWIDString[64] = '\0';

        memcpy(&g_HWID, g_HWIDBinary, sizeof(uint32_t));
    }

    std::once_flag g_HwidOnce;

    void EnsureHWID()
    {
        std::call_once(g_HwidOnce, GenerateHWID);
    }

    // ----- generic text helpers -------------------------------------------

    std::string TrimAscii(const std::string& s)
    {
        size_t b = 0;
        size_t e = s.size();
        while (b < e && std::isspace(static_cast<unsigned char>(s[b]))) ++b;
        while (e > b && std::isspace(static_cast<unsigned char>(s[e - 1]))) --e;
        return s.substr(b, e - b);
    }

    std::string ToLowerAscii(std::string s)
    {
        for (char& c : s)
        {
            c = static_cast<char>(std::tolower(static_cast<unsigned char>(c)));
        }
        return s;
    }

    size_t FirstContentOffset(const std::string& text)
    {
        size_t b = text.find_first_not_of(" \t\r");
        if (b == std::string::npos)
        {
            return b;
        }
        // Skip a UTF-8 BOM if the file starts with one.
        if (text.compare(b, 3, "\xEF\xBB\xBF") == 0)
        {
            b = text.find_first_not_of(" \t\r", b + 3);
        }
        return b;
    }

    bool ParseSectionHeader(const std::string& text, std::string& sectionLower)
    {
        size_t b = FirstContentOffset(text);
        if (b == std::string::npos || text[b] != '[')
        {
            return false;
        }
        size_t e = text.find(']', b + 1);
        if (e == std::string::npos)
        {
            return false;
        }
        sectionLower = ToLowerAscii(TrimAscii(text.substr(b + 1, e - b - 1)));
        return !sectionLower.empty();
    }

    bool ParseKeyValue(const std::string& text, std::string& keyLower, std::string& value)
    {
        size_t b = FirstContentOffset(text);
        if (b == std::string::npos)
        {
            return false;
        }
        const char c0 = text[b];
        if (c0 == ';' || c0 == '#' || c0 == '[')
        {
            return false;
        }
        size_t eq = text.find('=', b);
        if (eq == std::string::npos)
        {
            return false;
        }
        keyLower = ToLowerAscii(TrimAscii(text.substr(b, eq - b)));
        if (keyLower.empty())
        {
            return false;
        }
        for (char c : keyLower)
        {
            if (!(std::isalnum(static_cast<unsigned char>(c)) || c == '_'))
            {
                return false;
            }
        }
        value = TrimAscii(text.substr(eq + 1));
        return true;
    }

    struct Line
    {
        std::string text;
        std::string eol;
    };

    std::vector<Line> SplitLines(const std::string& data)
    {
        std::vector<Line> out;
        size_t start = 0;
        while (start < data.size())
        {
            size_t nl = data.find('\n', start);
            Line line;
            if (nl == std::string::npos)
            {
                line.text = data.substr(start);
                line.eol = "";
                out.push_back(line);
                break;
            }
            std::string chunk = data.substr(start, nl - start);
            if (!chunk.empty() && chunk.back() == '\r')
            {
                chunk.pop_back();
                line.eol = "\r\n";
            }
            else
            {
                line.eol = "\n";
            }
            line.text = chunk;
            out.push_back(line);
            start = nl + 1;
        }
        return out;
    }

    std::string JoinLines(const std::vector<Line>& lines)
    {
        std::string out;
        for (const auto& line : lines)
        {
            out += line.text;
            out += line.eol;
        }
        return out;
    }

    // ----- file I/O --------------------------------------------------------

    bool ReadAllBytes(const std::wstring& path, std::string& out)
    {
        HANDLE h = CreateFileW(path.c_str(), GENERIC_READ,
                               FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                               nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
        if (h == INVALID_HANDLE_VALUE)
        {
            return false;
        }
        out.clear();
        char buf[64 * 1024];
        DWORD read = 0;
        bool ok = true;
        for (;;)
        {
            if (!ReadFile(h, buf, sizeof(buf), &read, nullptr))
            {
                ok = false;
                break;
            }
            if (read == 0)
            {
                break;
            }
            out.append(buf, read);
        }
        CloseHandle(h);
        return ok;
    }

    std::wstring DirectoryOf(const std::wstring& path)
    {
        size_t slash = path.find_last_of(L"\\/");
        return (slash == std::wstring::npos) ? std::wstring() : path.substr(0, slash);
    }

    // Delete the stale "path.tmp" sibling plus any "path.tmp.*" leftovers the
    // old PID-suffixed writer may have stranded.
    void CleanupStaleTempFiles(const std::wstring& path)
    {
        DeleteFileW((path + L".tmp").c_str());

        const std::wstring dir = DirectoryOf(path);
        WIN32_FIND_DATAW fd = {};
        HANDLE hFind = FindFirstFileW((path + L".tmp.*").c_str(), &fd);
        if (hFind == INVALID_HANDLE_VALUE)
        {
            return;
        }
        do
        {
            if (fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)
            {
                continue;
            }
            std::wstring victim = dir.empty() ? std::wstring(fd.cFileName)
                                              : dir + L"\\" + fd.cFileName;
            DeleteFileW(victim.c_str());
        } while (FindNextFileW(hFind, &fd));
        FindClose(hFind);
    }

    bool WriteFileAtomic(const std::wstring& path, const std::string& data)
    {
        CleanupStaleTempFiles(path);

        const std::wstring tmpPath = path + L".tmp";
        HANDLE h = CreateFileW(tmpPath.c_str(), GENERIC_WRITE, 0, nullptr,
                               CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
        if (h == INVALID_HANDLE_VALUE)
        {
            OutputDebugStringW(L"[ZoneClient] Identity: cannot create temp file; original left untouched.\n");
            return false;
        }

        bool ok = true;
        DWORD writeErr = ERROR_SUCCESS;
        size_t offset = 0;
        while (offset < data.size())
        {
            size_t remaining = data.size() - offset;
            DWORD chunk = (remaining > (1 << 20)) ? (1 << 20) : static_cast<DWORD>(remaining);
            DWORD written = 0;
            if (!WriteFile(h, data.data() + offset, chunk, &written, nullptr) || written != chunk)
            {
                ok = false;
                writeErr = GetLastError();
                break;
            }
            offset += written;
        }
        BOOL flushed = ok ? FlushFileBuffers(h) : FALSE;
        DWORD flushErr = flushed ? ERROR_SUCCESS : GetLastError();
        CloseHandle(h);

        if (!ok || !flushed)
        {
            DeleteFileW(tmpPath.c_str());
            OutputDebugStringW(L"[ZoneClient] Identity: write failed; original left untouched.\n");
            return false;
        }

        if (!MoveFileExW(tmpPath.c_str(), path.c_str(),
                         MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH))
        {
            DWORD err = GetLastError();
            DeleteFileW(tmpPath.c_str());
            wchar_t msg[256];
            _snwprintf_s(msg, _TRUNCATE,
                         L"[ZoneClient] Identity: MoveFileExW failed (error %lu); original left untouched.\n",
                         static_cast<unsigned long>(err));
            OutputDebugStringW(msg);
            return false;
        }
        return true;
    }

    bool ResolveConfigPath()
    {
        std::wstring root = AssetProvisioner::GetGameRoot();
        if (!root.empty())
        {
            std::wstring appdata = root + L"\\appdata";
            CreateDirectoryW(appdata.c_str(), nullptr);
            g_ConfigPathW = appdata + L"\\zone_identity.ltx";
            return true;
        }

        wchar_t cwd[MAX_PATH] = {};
        if (GetCurrentDirectoryW(MAX_PATH, cwd) == 0)
        {
            return false;
        }
        std::wstring appdata = std::wstring(cwd) + L"\\appdata";
        CreateDirectoryW(appdata.c_str(), nullptr);
        g_ConfigPathW = appdata + L"\\zone_identity.ltx";
        return true;
    }

    // ----- canonical key merge --------------------------------------------

    bool IsUsableUuid(const std::string& raw)
    {
        std::string v = TrimAscii(raw);
        if (v.empty())
        {
            return false;
        }
        std::string lower = ToLowerAscii(v);
        return lower != "anonymous" && lower != "default_uuid" && lower != "default";
    }

    bool ParseHwidHash(const std::string& raw, uint32_t& out)
    {
        std::string v = TrimAscii(raw);
        if (v.empty())
        {
            return false;
        }
        unsigned long long acc = 0;
        for (char c : v)
        {
            if (c < '0' || c > '9')
            {
                return false;
            }
            acc = acc * 10ull + static_cast<unsigned long long>(c - '0');
            if (acc > 0xFFFFFFFFull)
            {
                return false;
            }
        }
        if (acc == 0)
        {
            return false;
        }
        out = static_cast<uint32_t>(acc);
        return true;
    }

    // Merge the three canonical keys into the given line set. Existing
    // canonical keys inside [zone_identity] are updated in place; keys missing
    // from that section are inserted there (creating the section when needed).
    // Every other line/section is emitted verbatim.
    std::string MergeCanonicalKeys(const std::vector<Line>& lines,
                                   const std::string& uuid,
                                   const std::string& hwidHash,
                                   const std::string& nickname)
    {
        static const char* const kKeys[3] = {"client_uuid", "hwid_hash", "nickname"};
        const std::string values[3] = {uuid, hwidHash, nickname};

        std::vector<Line> out = lines;
        const std::string defaultEol = lines.empty()
            ? std::string("\n")
            : (lines[0].eol.empty() ? std::string("\n") : lines[0].eol);

        size_t sectionIdx = static_cast<size_t>(-1);
        for (size_t i = 0; i < out.size(); ++i)
        {
            std::string sec;
            if (ParseSectionHeader(out[i].text, sec) && sec == "zone_identity")
            {
                sectionIdx = i;
                break;
            }
        }

        size_t sectionEnd = out.size();
        bool written[3] = {false, false, false};
        if (sectionIdx != static_cast<size_t>(-1))
        {
            for (size_t i = sectionIdx + 1; i < out.size(); ++i)
            {
                std::string sec;
                if (ParseSectionHeader(out[i].text, sec))
                {
                    sectionEnd = i;
                    break;
                }
                std::string key;
                std::string val;
                if (!ParseKeyValue(out[i].text, key, val))
                {
                    continue;
                }
                for (int k = 0; k < 3; ++k)
                {
                    if (!written[k] && key == kKeys[k])
                    {
                        if (val != values[k])
                        {
                            out[i].text = std::string(kKeys[k]) + " = " + values[k];
                        }
                        written[k] = true;
                        break;
                    }
                }
            }
        }

        std::vector<Line> missing;
        for (int k = 0; k < 3; ++k)
        {
            if (written[k])
            {
                continue;
            }
            Line line;
            line.text = std::string(kKeys[k]) + " = " + values[k];
            line.eol = defaultEol;
            missing.push_back(line);
        }

        if (!missing.empty())
        {
            if (sectionIdx != static_cast<size_t>(-1))
            {
                out.insert(out.begin() + static_cast<ptrdiff_t>(sectionEnd),
                           missing.begin(), missing.end());
            }
            else
            {
                if (!out.empty() && out.back().eol.empty())
                {
                    out.back().eol = defaultEol;
                }
                Line header;
                header.text = "[zone_identity]";
                header.eol = defaultEol;
                out.push_back(header);
                out.insert(out.end(), missing.begin(), missing.end());
            }
        }

        return JoinLines(out);
    }
}

namespace Identity
{
    void Init()
    {
        EnsureHWID();

        if (!ResolveConfigPath())
        {
            OutputDebugStringW(L"[ZoneClient] Identity: could not resolve zone_identity.ltx path.\n");
            return;
        }

        std::string data;
        const bool existed = ReadAllBytes(g_ConfigPathW, data);
        const std::vector<Line> lines = existed ? SplitLines(data) : std::vector<Line>();

        std::string uuidCanonical;
        std::string uuidOther;
        std::string uuidLegacy;
        std::string hwidCanonical;
        std::string hwidOther;
        std::string hwidLegacy;
        std::string nickCanonical;
        std::string nickOther;
        {
            std::string section;
            for (const auto& line : lines)
            {
                std::string sec;
                if (ParseSectionHeader(line.text, sec))
                {
                    section = sec;
                    continue;
                }
                std::string key;
                std::string val;
                if (!ParseKeyValue(line.text, key, val))
                {
                    continue;
                }
                const bool canonicalSection = (section == "zone_identity");
                if (key == "client_uuid")
                {
                    (canonicalSection ? uuidCanonical : uuidOther) = val;
                }
                else if (key == "uuid")
                {
                    uuidLegacy = val;
                }
                else if (key == "hwid_hash")
                {
                    (canonicalSection ? hwidCanonical : hwidOther) = val;
                }
                else if (key == "hwid")
                {
                    hwidLegacy = val;
                }
                else if (key == "nickname")
                {
                    (canonicalSection ? nickCanonical : nickOther) = val;
                }
            }
        }

        std::string uuid;
        if (IsUsableUuid(uuidCanonical)) uuid = TrimAscii(uuidCanonical);
        else if (IsUsableUuid(uuidOther)) uuid = TrimAscii(uuidOther);
        else if (IsUsableUuid(uuidLegacy)) uuid = TrimAscii(uuidLegacy);
        if (uuid.empty())
        {
            uuid = GenerateUUID();
        }

        std::string hwidHash;
        uint32_t parsedHash = 0;
        if (ParseHwidHash(hwidCanonical, parsedHash))
        {
            hwidHash = TrimAscii(hwidCanonical);
        }
        else if (ParseHwidHash(hwidOther, parsedHash))
        {
            hwidHash = TrimAscii(hwidOther);
        }
        else
        {
            // No canonical numeric hash yet: derive the stable hardware hash.
            // Legacy "hwid=" (64-char sha256 hex) is accepted as proof that an
            // identity already existed; the numeric form is computed from the
            // same hardware material, so nothing churns.
            if (!hwidLegacy.empty())
            {
                OutputDebugStringA("[ZoneClient] Identity: legacy hwid= found; using hardware-derived hwid_hash.\n");
            }
            hwidHash = std::to_string(g_HWID);
        }

        std::string nickname;
        if (!TrimAscii(nickCanonical).empty()) nickname = TrimAscii(nickCanonical);
        else if (!TrimAscii(nickOther).empty()) nickname = TrimAscii(nickOther);
        else nickname = "Stalker";

        {
            std::lock_guard<std::mutex> lock(g_IdentityMutex);
            g_UUID = uuid;
            g_HWIDHashString = hwidHash;
            g_Nickname = nickname;
        }

        const std::string merged = MergeCanonicalKeys(lines, uuid, hwidHash, nickname);
        if (!existed || merged != data)
        {
            if (!WriteFileAtomic(g_ConfigPathW, merged))
            {
                OutputDebugStringW(L"[ZoneClient] Identity: zone_identity.ltx not updated; previous content retained.\n");
            }
        }
    }

    std::string GetClientUUID()
    {
        std::lock_guard<std::mutex> lock(g_IdentityMutex);
        return g_UUID;
    }

    const uint8_t* GetHWIDBinary()
    {
        // Pure reader: one-time HWID computation is guarded by call_once.
        // The lazy fallback is routed through the same once-flag so concurrent
        // callers never race on g_HWIDBinary/g_HWIDString/g_HWID.
        EnsureHWID();
        return g_HWIDBinary;
    }

    const uint8_t* GetHWID()
    {
        return GetHWIDBinary();
    }

    const char* GetHWIDString()
    {
        // Pure reader: see GetHWIDBinary note; fallback via call_once only.
        EnsureHWID();
        return g_HWIDString;
    }

    uint32_t GetHWIDHash()
    {
        EnsureHWID();
        return g_HWID;
    }

    std::string GetNickname()
    {
        std::lock_guard<std::mutex> lock(g_IdentityMutex);
        return g_Nickname;
    }

    void SetNickname(const std::string& nick)
    {
        {
            std::lock_guard<std::mutex> lock(g_IdentityMutex);
            g_Nickname = nick;
        }
        Save();
    }

    void Save()
    {
        if (g_ConfigPathW.empty())
        {
            return;
        }

        std::string uuid;
        std::string hwidHash;
        std::string nickname;
        {
            std::lock_guard<std::mutex> lock(g_IdentityMutex);
            uuid = g_UUID;
            hwidHash = g_HWIDHashString;
            nickname = g_Nickname;
        }
        if (hwidHash.empty())
        {
            hwidHash = std::to_string(g_HWID);
        }

        // Re-read at write time so keys written by other processes (Lua
        // favorites/history) between Init and Save are preserved.
        std::string data;
        const bool existed = ReadAllBytes(g_ConfigPathW, data);
        const std::vector<Line> lines = existed ? SplitLines(data) : std::vector<Line>();

        const std::string merged = MergeCanonicalKeys(lines, uuid, hwidHash, nickname);
        if (!existed || merged != data)
        {
            WriteFileAtomic(g_ConfigPathW, merged);
        }
    }
}
