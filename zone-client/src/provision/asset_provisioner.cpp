// Zone Asset Provisioner
// Automatically provisions missing configuration, UI XML, and essential scripts into S.T.A.L.K.E.R. Anomaly game root.

#include "asset_provisioner.h"
#include "embedded_assets.h"
#include <windows.h>
#include <shlwapi.h>
#include <string>
#include <vector>

#pragma comment(lib, "shlwapi.lib")

namespace AssetProvisioner
{
    std::wstring GetGameRoot()
    {
        wchar_t buf[MAX_PATH] = {};
        DWORD ret = GetModuleFileNameW(nullptr, buf, MAX_PATH);
        if (ret == 0 || ret >= MAX_PATH)
            return L"";

        std::wstring path(buf);
        for (auto& c : path)
        {
            if (c == L'/') c = L'\\';
        }

        size_t last = path.find_last_of(L'\\');
        if (last != std::wstring::npos)
            path = path.substr(0, last);

        while (!path.empty() && path.back() == L'\\')
            path.pop_back();

        // Strip trailing binary subfolders (\x64, \Win64, \bin, \bin_x64, \bin_dedicated)
        bool stripped = true;
        while (stripped && !path.empty())
        {
            stripped = false;
            size_t slash = path.find_last_of(L'\\');
            if (slash != std::wstring::npos)
            {
                std::wstring folder = path.substr(slash + 1);
                if (_wcsicmp(folder.c_str(), L"x64") == 0 ||
                    _wcsicmp(folder.c_str(), L"Win64") == 0 ||
                    _wcsicmp(folder.c_str(), L"bin") == 0 ||
                    _wcsicmp(folder.c_str(), L"bin_x64") == 0 ||
                    _wcsicmp(folder.c_str(), L"bin_dedicated") == 0)
                {
                    path = path.substr(0, slash);
                    stripped = true;
                    while (!path.empty() && path.back() == L'\\')
                        path.pop_back();
                }
            }
        }
        return path;
    }

    bool FileExists(const std::wstring& path)
    {
        DWORD attrib = GetFileAttributesW(path.c_str());
        return (attrib != INVALID_FILE_ATTRIBUTES && !(attrib & FILE_ATTRIBUTE_DIRECTORY));
    }

    bool EnsureDirectoryTree(const std::wstring& dirPath)
    {
        if (dirPath.empty()) return true;
        
        DWORD attrib = GetFileAttributesW(dirPath.c_str());
        if (attrib != INVALID_FILE_ATTRIBUTES && (attrib & FILE_ATTRIBUTE_DIRECTORY))
            return true;

        size_t slash = dirPath.find_last_of(L"\\/");
        if (slash != std::wstring::npos)
            EnsureDirectoryTree(dirPath.substr(0, slash));

        return CreateDirectoryW(dirPath.c_str(), nullptr) || GetLastError() == ERROR_ALREADY_EXISTS;
    }

    bool WriteFileIfMissing(const std::wstring& path, const std::string& content)
    {
        if (FileExists(path))
            return true; // Already exists, don't overwrite user customizations

        size_t slash = path.find_last_of(L"\\/");
        if (slash != std::wstring::npos)
            EnsureDirectoryTree(path.substr(0, slash));

        // Wide/Win32 write so non-ASCII game paths work (no narrow ofstream).
        HANDLE h = CreateFileW(path.c_str(), GENERIC_WRITE, 0, nullptr,
                               CREATE_NEW, FILE_ATTRIBUTE_NORMAL, nullptr);
        if (h == INVALID_HANDLE_VALUE)
        {
            DWORD err = GetLastError();
            if (err == ERROR_ALREADY_EXISTS || err == ERROR_FILE_EXISTS)
                return true; // Raced with another writer; user file wins.
            std::wstring msg = L"[AssetProvisioner] WriteFileIfMissing: CreateFileW failed for \"" +
                path + L"\" (error " + std::to_wstring(err) + L")\n";
            OutputDebugStringW(msg.c_str());
            return false;
        }

        bool ok = true;
        DWORD werr = ERROR_SUCCESS;
        size_t offset = 0;
        while (offset < content.size())
        {
            size_t remaining = content.size() - offset;
            DWORD chunk = (remaining > (1 << 20)) ? (1 << 20) : static_cast<DWORD>(remaining);
            DWORD written = 0;
            if (!WriteFile(h, content.data() + offset, chunk, &written, nullptr) || written != chunk)
            {
                ok = false;
                werr = GetLastError();
                break;
            }
            offset += written;
        }
        // Capture flush result BEFORE CloseHandle (which may reset LastError).
        BOOL flushed = FlushFileBuffers(h);
        DWORD ferr = flushed ? ERROR_SUCCESS : GetLastError();
        CloseHandle(h);

        if (!ok || !flushed)
        {
            DWORD err = !ok ? werr : ferr;
            DeleteFileW(path.c_str());
            std::wstring msg = L"[AssetProvisioner] WriteFileIfMissing: WriteFile failed for \"" +
                path + L"\" (error " + std::to_wstring(err) + L")\n";
            OutputDebugStringW(msg.c_str());
            return false;
        }
        return true;
    }

    bool OverwriteFile(const std::wstring& path, const std::string& content)
    {
        size_t slash = path.find_last_of(L"\\/");
        if (slash != std::wstring::npos)
            EnsureDirectoryTree(path.substr(0, slash));

        // Atomic write: temp file + MoveFileExW(REPLACE_EXISTING) so a crash
        // mid-write never leaves a truncated/zero-byte target. Old file is
        // kept on any failure. PID-suffixed temp name so two processes sharing
        // one game root never interleave chunks into the same .tmp.
        std::wstring tmpPath = path + L".tmp." + std::to_wstring(GetCurrentProcessId());

        HANDLE h = CreateFileW(tmpPath.c_str(), GENERIC_WRITE, 0, nullptr,
                               CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
        if (h == INVALID_HANDLE_VALUE)
        {
            DWORD err = GetLastError();
            std::wstring msg = L"[AssetProvisioner] OverwriteFile: CreateFileW(temp) failed for \"" +
                path + L"\" (error " + std::to_wstring(err) + L"); keeping old file.\n";
            OutputDebugStringW(msg.c_str());
            return false;
        }

        bool ok = true;
        DWORD werr = ERROR_SUCCESS;
        size_t offset = 0;
        while (offset < content.size())
        {
            size_t remaining = content.size() - offset;
            DWORD chunk = (remaining > (1 << 20)) ? (1 << 20) : static_cast<DWORD>(remaining);
            DWORD written = 0;
            if (!WriteFile(h, content.data() + offset, chunk, &written, nullptr) || written != chunk)
            {
                ok = false;
                werr = GetLastError();
                break;
            }
            offset += written;
        }
        // Capture flush result BEFORE CloseHandle (which may reset LastError).
        BOOL flushed = FlushFileBuffers(h);
        DWORD ferr = flushed ? ERROR_SUCCESS : GetLastError();
        CloseHandle(h);

        if (!ok || !flushed)
        {
            DWORD err = !ok ? werr : ferr;
            DeleteFileW(tmpPath.c_str());
            std::wstring msg = L"[AssetProvisioner] OverwriteFile: WriteFile failed for \"" +
                path + L"\" (error " + std::to_wstring(err) + L"); keeping old file.\n";
            OutputDebugStringW(msg.c_str());
            return false;
        }

        if (!MoveFileExW(tmpPath.c_str(), path.c_str(),
                         MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH))
        {
            DWORD err = GetLastError();
            DeleteFileW(tmpPath.c_str());
            std::wstring msg = L"[AssetProvisioner] OverwriteFile: MoveFileExW failed for \"" +
                path + L"\" (error " + std::to_wstring(err) + L"); keeping old file.\n";
            OutputDebugStringW(msg.c_str());
            return false;
        }
        return true;
    }

    void EnsureAssets()
    {
        std::wstring root = GetGameRoot();
        if (root.empty())
        {
            OutputDebugStringW(L"[AssetProvisioner] GetGameRoot returned empty - cannot provision assets (game root unknown). Check process image path.\n");
            return;
        }

        // 1. Iterate through compile-time embedded assets.
        //    Script files and UI XML files are always overwritten (mod code and UI layout must match DLL).
        //    Config files are written only if missing (preserve user customizations).
        for (size_t i = 0; i < g_EmbeddedAssetsCount; ++i)
        {
            const auto& asset = g_EmbeddedAssets[i];
            std::wstring fullPath = root + L"\\" + asset.relativePath;
            std::string content(asset.data, asset.size);

            // Detect script files and UI XML files — always overwrite
            std::wstring rel(asset.relativePath);
            bool isScript = (rel.size() >= 7 && rel.compare(rel.size() - 7, 7, L".script") == 0);
            bool isUiXml  = (rel.find(L"zone_ui_server_list.xml") != std::wstring::npos) ||
                            (rel.find(L"configs\\ui\\") != std::wstring::npos);

            if (isScript || isUiXml)
                OverwriteFile(fullPath, content);
            else
                WriteFileIfMissing(fullPath, content);
        }

        // 2. UI layout enforcement: ensure zone_ui_server_list.xml is overwritten with the latest layout
        std::wstring xmlPath = root + L"\\gamedata\\configs\\ui\\zone_ui_server_list.xml";
        OverwriteFile(xmlPath, std::string(g_CleanServerListXml, g_CleanServerListXmlSize));
    }
}
