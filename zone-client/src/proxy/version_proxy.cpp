// Drop-in version.dll proxy for ZoneClient.
//
// Installation: copy the built version.dll into the game's bin directory
// (next to AnomalyDX9/DX10/DX11.exe and the AVX variants) and place
// ZoneClient.dll beside it, then copy gamedata\ to the game root as usual.
//
// Every Anomaly executable statically imports VERSION.dll, and "version" is
// not in the HKLM KnownDLLs list, so the loader resolves this copy from the
// application directory before System32.
//
// The proxy does exactly two things:
//   1. Forward all 15 VERSION.dll exports to the real system DLL.
//   2. Asynchronously load <own directory>\ZoneClient.dll once it exists.
// It installs no hooks and performs no file I/O under the loader lock;
// ZoneClient.dll does the rest.
//
// Export forwarding uses explicit absolute System32 paths. Forwarding to
// "version.<name>" instead would re-enter this very module (the loader keys
// a forwarder by module name, which is now us) and fail with a loop.

#include <windows.h>
#include <string.h>

#pragma comment(linker, "/export:GetFileVersionInfoA=C:\\Windows\\System32\\version.GetFileVersionInfoA")
#pragma comment(linker, "/export:GetFileVersionInfoByHandle=C:\\Windows\\System32\\version.GetFileVersionInfoByHandle")
#pragma comment(linker, "/export:GetFileVersionInfoExA=C:\\Windows\\System32\\version.GetFileVersionInfoExA")
#pragma comment(linker, "/export:GetFileVersionInfoExW=C:\\Windows\\System32\\version.GetFileVersionInfoExW")
#pragma comment(linker, "/export:GetFileVersionInfoSizeA=C:\\Windows\\System32\\version.GetFileVersionInfoSizeA")
#pragma comment(linker, "/export:GetFileVersionInfoSizeExA=C:\\Windows\\System32\\version.GetFileVersionInfoSizeExA")
#pragma comment(linker, "/export:GetFileVersionInfoSizeExW=C:\\Windows\\System32\\version.GetFileVersionInfoSizeExW")
#pragma comment(linker, "/export:GetFileVersionInfoSizeW=C:\\Windows\\System32\\version.GetFileVersionInfoSizeW")
#pragma comment(linker, "/export:GetFileVersionInfoW=C:\\Windows\\System32\\version.GetFileVersionInfoW")
#pragma comment(linker, "/export:VerFindFileA=C:\\Windows\\System32\\version.VerFindFileA")
#pragma comment(linker, "/export:VerFindFileW=C:\\Windows\\System32\\version.VerFindFileW")
#pragma comment(linker, "/export:VerInstallFileA=C:\\Windows\\System32\\version.VerInstallFileA")
#pragma comment(linker, "/export:VerInstallFileW=C:\\Windows\\System32\\version.VerInstallFileW")
#pragma comment(linker, "/export:VerQueryValueA=C:\\Windows\\System32\\version.VerQueryValueA")
#pragma comment(linker, "/export:VerQueryValueW=C:\\Windows\\System32\\version.VerQueryValueW")

namespace
{
    constexpr DWORD kLoadTimeoutMs  = 15000;
    constexpr DWORD kPollIntervalMs = 100;

    void Log(const wchar_t* message)
    {
        OutputDebugStringW(message);
    }

    // Manual unsigned-to-decimal conversion; keeps the proxy free of any
    // user32 import (wsprintfW) and of CRT formatting code.
    void LogWin32(const wchar_t* message, DWORD error)
    {
        wchar_t digits[12];
        int first = 11;
        digits[first] = L'\0';
        do
        {
            digits[--first] = static_cast<wchar_t>(L'0' + (error % 10));
            error /= 10;
        } while (error != 0);

        OutputDebugStringW(message);
        OutputDebugStringW(L" (error ");
        OutputDebugStringW(digits + first);
        OutputDebugStringW(L")\n");
    }

    void LogPath(const wchar_t* prefix, const wchar_t* path)
    {
        OutputDebugStringW(prefix);
        OutputDebugStringW(path);
        OutputDebugStringW(L"\n");
    }

    // Wait (bounded) for <proxy directory>\ZoneClient.dll, then load it.
    // Runs on its own thread created from DllMain, never under the loader lock.
    DWORD WINAPI ClientLoaderThread(LPVOID lpParam)
    {
        HMODULE hSelf = reinterpret_cast<HMODULE>(lpParam);

        wchar_t clientPath[32768] = {};
        DWORD len = GetModuleFileNameW(hSelf, clientPath, ARRAYSIZE(clientPath));
        if (len == 0 || len >= ARRAYSIZE(clientPath))
        {
            LogWin32(L"[ZoneVersionProxy] GetModuleFileNameW failed", GetLastError());
            return 0;
        }

        // Trim the module file name back to (and including) the last separator.
        DWORD dirLen = len;
        while (dirLen > 0 && clientPath[dirLen - 1] != L'\\' && clientPath[dirLen - 1] != L'/')
        {
            --dirLen;
        }
        if (dirLen == 0)
        {
            Log(L"[ZoneVersionProxy] Module path has no directory separator; cannot locate ZoneClient.dll\n");
            return 0;
        }

        static const wchar_t kClientName[] = L"ZoneClient.dll";
        if (static_cast<size_t>(dirLen) + ARRAYSIZE(kClientName) > ARRAYSIZE(clientPath))
        {
            Log(L"[ZoneVersionProxy] Module path too long; cannot append ZoneClient.dll\n");
            return 0;
        }
        memcpy(clientPath + dirLen, kClientName, sizeof(kClientName));

        for (DWORD waited = 0; waited <= kLoadTimeoutMs; waited += kPollIntervalMs)
        {
            DWORD attrib = GetFileAttributesW(clientPath);
            if (attrib != INVALID_FILE_ATTRIBUTES && !(attrib & FILE_ATTRIBUTE_DIRECTORY))
            {
                HMODULE hClient = LoadLibraryW(clientPath);
                if (hClient)
                {
                    LogPath(L"[ZoneVersionProxy] Loaded ", clientPath);
                    return 0;
                }

                DWORD err = GetLastError();
                // A concurrent copy can leave the file present but locked;
                // keep retrying within the deadline for those errors only.
                if (err != ERROR_SHARING_VIOLATION &&
                    err != ERROR_LOCK_VIOLATION &&
                    err != ERROR_ACCESS_DENIED &&
                    err != ERROR_BAD_EXE_FORMAT)
                {
                    LogPath(L"[ZoneVersionProxy] LoadLibraryW failed for ", clientPath);
                    LogWin32(L"[ZoneVersionProxy] LoadLibraryW error", err);
                    return 0;
                }
            }
            Sleep(kPollIntervalMs);
        }

        LogPath(L"[ZoneVersionProxy] Timed out waiting for ZoneClient.dll: ", clientPath);
        return 0;
    }
}

BOOL WINAPI DllMain(HINSTANCE hinstDLL, DWORD fdwReason, LPVOID lpvReserved)
{
    UNREFERENCED_PARAMETER(lpvReserved);

    switch (fdwReason)
    {
        case DLL_PROCESS_ATTACH:
        {
            DisableThreadLibraryCalls(hinstDLL);

            HANDLE hThread = CreateThread(nullptr, 0, ClientLoaderThread,
                                          reinterpret_cast<LPVOID>(hinstDLL), 0, nullptr);
            if (hThread)
            {
                CloseHandle(hThread);
            }
            else
            {
                LogWin32(L"[ZoneVersionProxy] CreateThread failed", GetLastError());
            }
            break;
        }

        case DLL_PROCESS_DETACH:
        default:
            break;
    }

    return TRUE;
}
