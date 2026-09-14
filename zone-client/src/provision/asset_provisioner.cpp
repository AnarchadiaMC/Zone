// Zone Asset Provisioner
// Automatically provisions missing configuration, UI XML, and essential scripts into S.T.A.L.K.E.R. Anomaly game root.

#include "asset_provisioner.h"
#include "embedded_assets.h"
#include <windows.h>
#include <shlwapi.h>
#include <string>
#include <fstream>
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

        std::ofstream file(path.c_str(), std::ios::out | std::ios::binary);
        if (!file.is_open())
            return false;

        file.write(content.data(), content.size());
        return true;
    }

    bool OverwriteFile(const std::wstring& path, const std::string& content)
    {
        size_t slash = path.find_last_of(L"\\/");
        if (slash != std::wstring::npos)
            EnsureDirectoryTree(path.substr(0, slash));

        std::ofstream file(path.c_str(), std::ios::out | std::ios::binary | std::ios::trunc);
        if (!file.is_open())
            return false;

        file.write(content.data(), content.size());
        return true;
    }

    void EnsureAssets()
    {
        std::wstring root = GetGameRoot();
        if (root.empty()) return;

        // 1. Iterate through compile-time embedded assets and write if missing
        for (size_t i = 0; i < g_EmbeddedAssetsCount; ++i)
        {
            const auto& asset = g_EmbeddedAssets[i];
            std::wstring fullPath = root + L"\\" + asset.relativePath;
            WriteFileIfMissing(fullPath, std::string(asset.data, asset.size));
        }

        // 2. Sanitization: check if gamedata\\configs\\ui\\zone_ui_server_list.xml exists on disk
        // and contains legacy '<btn_zone_online'. If found, overwrite it with clean server list XML.
        std::wstring xmlPath = root + L"\\gamedata\\configs\\ui\\zone_ui_server_list.xml";
        if (FileExists(xmlPath))
        {
            std::ifstream in(xmlPath.c_str(), std::ios::in | std::ios::binary);
            if (in.is_open())
            {
                std::string content((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
                in.close();
                if (content.find("<btn_zone_online") != std::string::npos)
                {
                    OverwriteFile(xmlPath, std::string(g_CleanServerListXml, g_CleanServerListXmlSize));
                }
            }
        }
    }
}
