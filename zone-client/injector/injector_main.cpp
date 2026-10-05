#include <windows.h>
#include <tlhelp32.h>
#include <psapi.h>
#include <iostream>
#include <string>
#include <vector>
#include <algorithm>

#pragma comment(lib, "advapi32.lib")
#pragma comment(lib, "psapi.lib")

// ---------------------------------------------------------------------------
// Privilege Elevation: Enable SeDebugPrivilege if available
// ---------------------------------------------------------------------------
static bool EnableDebugPrivilege()
{
    HANDLE hToken = nullptr;
    if (!OpenProcessToken(GetCurrentProcess(), TOKEN_ADJUST_PRIVILEGES | TOKEN_QUERY, &hToken))
    {
        return false;
    }

    LUID luid;
    if (!LookupPrivilegeValueW(nullptr, SE_DEBUG_NAME, &luid))
    {
        CloseHandle(hToken);
        return false;
    }

    TOKEN_PRIVILEGES tp;
    tp.PrivilegeCount = 1;
    tp.Privileges[0].Luid = luid;
    tp.Privileges[0].Attributes = SE_PRIVILEGE_ENABLED;

    BOOL ok = AdjustTokenPrivileges(hToken, FALSE, &tp, sizeof(TOKEN_PRIVILEGES), nullptr, nullptr);
    CloseHandle(hToken);
    return ok && (GetLastError() == ERROR_SUCCESS);
}

// ---------------------------------------------------------------------------
// Utility: Check if a file exists on disk
// ---------------------------------------------------------------------------
static bool FileExists(const std::wstring& path)
{
    DWORD attr = GetFileAttributesW(path.c_str());
    return (attr != INVALID_FILE_ATTRIBUTES && !(attr & FILE_ATTRIBUTE_DIRECTORY));
}

static bool DirectoryExists(const std::wstring& path)
{
    DWORD attr = GetFileAttributesW(path.c_str());
    return (attr != INVALID_FILE_ATTRIBUTES && (attr & FILE_ATTRIBUTE_DIRECTORY));
}

static std::wstring GetFullPath(const std::wstring& relPath);

static void EnsureDir(const std::wstring& dir)
{
    if (dir.empty())
    {
        return;
    }
    for (size_t i = 0; i < dir.size(); ++i)
    {
        if (dir[i] == L'\\' || dir[i] == L'/')
        {
            if (i <= 3 && dir.size() > 1 && dir[1] == L':')
            {
                continue;
            }
            std::wstring part = dir.substr(0, i);
            if (!part.empty())
            {
                CreateDirectoryW(part.c_str(), nullptr);
            }
        }
    }
    CreateDirectoryW(dir.c_str(), nullptr);
}

// Complete pre-launch asset manifest. Keep this list in sync with MOD_FILES in
// zone-client/tools/generate_embedded_assets.py (the DLL-side embedded-asset
// manifest used by AssetProvisioner::EnsureAssets). If a file is added or
// removed in one list, do the same in the other.
static const wchar_t* kProvisionedFiles[] = {
    L"gamedata\\configs\\mod_system_zone_online.ltx",
    L"gamedata\\configs\\system.ltx_patch.ltx",
    L"gamedata\\configs\\mod_system_zone_faction_relations.ltx",
    L"gamedata\\configs\\ui\\zone_ui_server_list.xml",
    L"gamedata\\configs\\ui\\ui_mm_zone_peer_faction.xml",
    L"gamedata\\configs\\ui\\zone_ui_chat.xml",
    L"gamedata\\configs\\text\\eng\\ui_zone.xml",
    L"gamedata\\configs\\text\\rus\\ui_zone.xml",
    L"gamedata\\scripts\\modxml_zone_main_menu.script",
    L"gamedata\\scripts\\zone_menu_patch.script",
    L"gamedata\\scripts\\zone_net.script",
    L"gamedata\\scripts\\zone_main.script",
    L"gamedata\\scripts\\zone_ui_server_list.script",
    L"gamedata\\scripts\\zone_ui_peer_faction.script",
    L"gamedata\\scripts\\zone_ai_proxy.script",
    L"gamedata\\scripts\\zone_dummy.script",
    L"gamedata\\scripts\\zone_hud.script",
    L"gamedata\\scripts\\zone_safezone.script",
    L"gamedata\\scripts\\zone_worldevent.script"
};
static const size_t kProvisionedFileCount = sizeof(kProvisionedFiles) / sizeof(kProvisionedFiles[0]);

// Locate the source gamedata directory shipped next to the injector (dist
// layout: dist/zone-online-vX.Y.Z/{client,gamedata}). Probes in order and
// stops at the first candidate that holds a gamedata directory or an
// fsgame.ltx marker.
static std::wstring FindSourceGamedata(const std::wstring& injDir)
{
    const std::wstring candidates[] = {
        injDir + L"\\gamedata\\",
        injDir + L"\\..\\gamedata\\",
        injDir + L"\\..\\..\\gamedata\\",
        injDir + L"\\..\\..\\..\\gamedata\\",
    };

    for (const auto& cand : candidates)
    {
        const std::wstring full = GetFullPath(cand);
        const bool hasGamedata = DirectoryExists(full);
        const bool hasFsgame = FileExists(GetFullPath(cand + L"..\\fsgame.ltx"));
        std::wcout << L"[*] Asset source probe: " << full
                   << (hasGamedata ? L" [gamedata dir found]"
                                   : (hasFsgame ? L" [fsgame.ltx found]" : L" [not found]"))
                   << L"\n";
        if (hasGamedata || hasFsgame)
        {
            return full;
        }
    }
    return L"";
}

// Pre-provision the mod's gamedata assets before launching the game.
//
// NOTE: this runs only for --launch. In --wait/--pid mode the engine is
// already running and has cached its filesystem mounts (fsgame.ltx), so any
// file copied now would not be seen until the game is restarted. We document
// that here and deliberately do not attempt a live filesystem rescan.
static void PreProvisionAssets(const std::wstring& gameRoot, const std::wstring& injectorDir)
{
    if (gameRoot.empty())
    {
        return;
    }
    std::wstring injDir = injectorDir;
    if (injDir.empty())
    {
        wchar_t exePath[MAX_PATH] = {};
        GetModuleFileNameW(nullptr, exePath, MAX_PATH);
        injDir = exePath;
        size_t lastSlash = injDir.find_last_of(L"\\/");
        if (lastSlash != std::wstring::npos)
        {
            injDir = injDir.substr(0, lastSlash);
        }
    }
    if (injDir.empty())
    {
        return;
    }

    std::wstring srcBase = FindSourceGamedata(injDir);
    if (srcBase.empty())
    {
        std::wcout << L"[*] Asset pre-provision skipped (no source gamedata found).\n";
        return;
    }
    if (srcBase.back() != L'\\' && srcBase.back() != L'/')
    {
        srcBase += L"\\";
    }
    std::wcout << L"[*] Provisioning from: " << srcBase << L"\n";

    int copied = 0;
    int skipped = 0;
    int failed = 0;
    for (size_t i = 0; i < kProvisionedFileCount; ++i)
    {
        std::wstring relW(kProvisionedFiles[i]);
        std::wstring sub = relW;
        if (sub.compare(0, 9, L"gamedata\\") == 0)
        {
            sub = sub.substr(9);
        }
        std::wstring src = srcBase + sub;
        std::wstring dst = gameRoot + L"\\" + relW;
        size_t slash = dst.find_last_of(L"\\/");
        if (slash != std::wstring::npos)
        {
            EnsureDir(dst.substr(0, slash));
        }
        if (CopyFileW(src.c_str(), dst.c_str(), FALSE))
        {
            ++copied;
            std::wcout << L"[+] Provisioned: " << relW << L"\n";
        }
        else
        {
            DWORD err = GetLastError();
            if (err == ERROR_FILE_NOT_FOUND || err == ERROR_PATH_NOT_FOUND)
            {
                ++skipped;
                std::wcout << L"[=] Skipped (source missing): " << relW << L"\n";
            }
            else
            {
                ++failed;
                std::wcout << L"[!] Pre-provision copy failed: " << src << L" -> " << dst
                           << L" (error " << err << L")\n";
            }
        }
    }
    std::wcout << L"[*] Asset pre-provision: " << copied << L" provisioned, "
               << skipped << L" skipped, " << failed << L" failed.\n";
}

static int PurgeProvisionedFiles(const std::wstring& gameRoot)
{
    if (gameRoot.empty())
    {
        return 1;
    }
    int removed = 0;
    int missing = 0;
    int errors = 0;
    for (size_t i = 0; i < kProvisionedFileCount; ++i)
    {
        std::wstring rel(kProvisionedFiles[i]);
        std::wstring path = gameRoot + L"\\" + rel;
        if (!FileExists(path))
        {
            ++missing;
        }
        else if (DeleteFileW(path.c_str()))
        {
            ++removed;
        }
        else
        {
            DWORD err = GetLastError();
            if (err == ERROR_FILE_NOT_FOUND || err == ERROR_PATH_NOT_FOUND)
            {
                ++missing;
            }
            else
            {
                std::wcerr << L"[!] Cleanup failed: " << path << L" (error " << err << L")\n";
                ++errors;
            }
        }
        std::wstring pattern = gameRoot + L"\\" + rel + L".tmp.*";
        WIN32_FIND_DATAW fd = {};
        HANDLE hFind = FindFirstFileW(pattern.c_str(), &fd);
        if (hFind != INVALID_HANDLE_VALUE)
        {
            std::wstring dir = gameRoot;
            size_t slash = rel.find_last_of(L"\\/");
            if (slash != std::wstring::npos)
            {
                dir = gameRoot + L"\\" + rel.substr(0, slash);
            }
            do
            {
                if (fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)
                {
                    continue;
                }
                std::wstring tmpPath = dir + L"\\" + fd.cFileName;
                if (DeleteFileW(tmpPath.c_str()))
                {
                    ++removed;
                }
                else
                {
                    DWORD err = GetLastError();
                    if (err != ERROR_FILE_NOT_FOUND && err != ERROR_PATH_NOT_FOUND)
                    {
                        std::wcerr << L"[!] Cleanup failed: " << tmpPath << L" (error " << err << L")\n";
                        ++errors;
                    }
                }
            } while (FindNextFileW(hFind, &fd));
            FindClose(hFind);
        }
    }
    std::wcout << L"[+] Cleanup: " << removed << L" removed, " << missing << L" already absent\n";
    return (errors == 0) ? 0 : 1;
}

// ---------------------------------------------------------------------------
// Utility: Resolve path to full absolute path
// ---------------------------------------------------------------------------
static std::wstring GetFullPath(const std::wstring& relPath)
{
    wchar_t buf[MAX_PATH] = {};
    DWORD len = GetFullPathNameW(relPath.c_str(), MAX_PATH, buf, nullptr);
    if (len > 0 && len < MAX_PATH)
    {
        return std::wstring(buf);
    }
    return relPath;
}

// ---------------------------------------------------------------------------
// Utility: default DLL path (ZoneClient.dll in same directory as injector)
// ---------------------------------------------------------------------------
static std::wstring DefaultDllPath()
{
    wchar_t exePath[MAX_PATH] = {};
    GetModuleFileNameW(nullptr, exePath, MAX_PATH);
    std::wstring path(exePath);
    size_t lastSlash = path.find_last_of(L"\\/");
    if (lastSlash != std::wstring::npos)
    {
        path = path.substr(0, lastSlash);
    }
    path += L"\\ZoneClient.dll";
    return GetFullPath(path);
}

// ---------------------------------------------------------------------------
// Utility: resolve the target process image directory
// ---------------------------------------------------------------------------
static std::wstring GetProcessImageDir(HANDLE hProcess)
{
    wchar_t buf[32768] = {};
    DWORD size = static_cast<DWORD>(sizeof(buf) / sizeof(buf[0]));
    if (!QueryFullProcessImageNameW(hProcess, 0, buf, &size) || size == 0)
    {
        return L"";
    }
    std::wstring path(buf, size);
    size_t lastSlash = path.find_last_of(L"\\/");
    if (lastSlash == std::wstring::npos)
    {
        return L"";
    }
    return path.substr(0, lastSlash);
}

static bool PathsEqualNoCase(const std::wstring& a, const std::wstring& b)
{
    return _wcsicmp(a.c_str(), b.c_str()) == 0;
}

// ---------------------------------------------------------------------------
// Deploy ZoneClient.dll next to the target game executable.
//
// zone_net.script resolves the DLL with ffi.load("ZoneClient"), i.e. plain
// LoadLibrary search, whose first hit is the game exe directory. The injector
// therefore stages a copy as <exeDir>\ZoneClient.dll and injects THAT path.
// Returns the path to inject: the staged copy on success, the original path
// (with a warning) when staging fails.
// ---------------------------------------------------------------------------
static std::wstring DeployDllBesideTarget(HANDLE hProcess, const std::wstring& rawDllPath)
{
    std::wstring dllPath = GetFullPath(rawDllPath);
    if (!FileExists(dllPath))
    {
        return dllPath; // InjectDLL reports the missing DLL.
    }

    const std::wstring exeDir = GetProcessImageDir(hProcess);
    if (exeDir.empty())
    {
        std::wcout << L"[!] Could not resolve target image directory; injecting original DLL path.\n"
                   << L"    ffi.load(\"ZoneClient\") may fail: Connect needs ZoneClient.dll next to the game exe.\n";
        return dllPath;
    }

    const std::wstring stagedPath = exeDir + L"\\ZoneClient.dll";
    if (PathsEqualNoCase(stagedPath, dllPath))
    {
        std::wcout << L"[*] DLL already next to target exe: " << stagedPath << L"\n";
        return dllPath;
    }

    // Overwrite unconditionally: the copy next to the exe must match this
    // injector build; a stale DLL there is the common Connect-fails cause.
    if (CopyFileW(dllPath.c_str(), stagedPath.c_str(), TRUE))
    {
        std::wcout << L"[*] Deployed DLL next to target exe: " << stagedPath << L"\n";
        return stagedPath;
    }

    DWORD err = GetLastError();
    std::wcout << L"[!] WARNING: failed to copy DLL to " << stagedPath << L" (error " << err << L").\n"
               << L"    Injecting the original path instead. ffi.load(\"ZoneClient\") may fail and Connect will not work\n"
               << L"    until ZoneClient.dll can be placed next to the game executable.\n";
    return dllPath;
}

// ---------------------------------------------------------------------------
// Utility: find a process by exe name (case-insensitive)
// ---------------------------------------------------------------------------
static DWORD FindProcessId(const std::wstring& processName)
{
    HANDLE snapshot = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
    if (snapshot == INVALID_HANDLE_VALUE)
    {
        return 0;
    }

    PROCESSENTRY32W pe;
    pe.dwSize = sizeof(pe);

    DWORD pid = 0;
    if (Process32FirstW(snapshot, &pe))
    {
        do
        {
            if (_wcsicmp(pe.szExeFile, processName.c_str()) == 0)
            {
                pid = pe.th32ProcessID;
                break;
            }
        } while (Process32NextW(snapshot, &pe));
    }

    CloseHandle(snapshot);
    return pid;
}

// ---------------------------------------------------------------------------
// Bitness check + engine-readiness gate (minimal scoped helpers)
// ---------------------------------------------------------------------------
static bool IsOS64Bit()
{
    SYSTEM_INFO si = {};
    GetNativeSystemInfo(&si);
    return (si.wProcessorArchitecture == PROCESSOR_ARCHITECTURE_AMD64 ||
            si.wProcessorArchitecture == PROCESSOR_ARCHITECTURE_ARM64);
}

static const wchar_t* InjectorArchName()
{
    return (sizeof(void*) == 8) ? L"x64" : L"x86";
}

// Fail fast on injector-vs-target architecture mismatch.
// DLL is built alongside the injector, so injector bitness == DLL bitness.
static bool CheckBitnessCompatibility(HANDLE hProcess, DWORD pid)
{
    BOOL targetWow64 = FALSE;
    if (!IsWow64Process(hProcess, &targetWow64))
    {
        // Fail open: could not query target; let injection attempt proceed
        // and surface any real error from CreateRemoteThread/LoadLibraryW.
        return true;
    }

    if (!IsOS64Bit())
    {
        return true; // 32-bit OS: everything is 32-bit, always compatible.
    }

    const bool selfIs64 = (sizeof(void*) == 8);
    const bool targetIs64 = (targetWow64 == FALSE);

    if (selfIs64 == targetIs64)
    {
        return true;
    }

    std::wcerr << L"[!] Bitness mismatch: injector/DLL is " << InjectorArchName()
               << L" but target PID " << pid
               << (targetIs64 ? L" is x64." : L" is x86 (Wow64).")
               << L"\n    Cannot inject "
               << (selfIs64 ? L"x64 DLL into x86 process." : L"x86 DLL into x64 process.")
               << L"\n    Note: AnomalyDX11/DX10 exes are x64; DX8/DX9-era exes in --wait list may be 32-bit - use a matching injector/DLL build.\n";
    return false;
}

struct MainWindowSearch
{
    DWORD pid = 0;
    bool found = false;
};

static BOOL CALLBACK EnumMainWindowCb(HWND hwnd, LPARAM lParam)
{
    auto* state = reinterpret_cast<MainWindowSearch*>(lParam);
    DWORD wpid = 0;
    GetWindowThreadProcessId(hwnd, &wpid);
    if (wpid != state->pid)
    {
        return TRUE;
    }
    if (!IsWindowVisible(hwnd))
    {
        return TRUE;
    }
    if (GetWindowTextLengthW(hwnd) == 0)
    {
        return TRUE;
    }
    state->found = true;
    return FALSE;
}

static bool TargetHasMainWindow(DWORD pid)
{
    if (pid == 0)
    {
        return false;
    }
    MainWindowSearch state;
    state.pid = pid;
    EnumWindows(EnumMainWindowCb, reinterpret_cast<LPARAM>(&state));
    return state.found;
}

// Bounded readiness gate. xray-monolith links LuaJIT statically, so there is
// no LuaJIT.dll / lua51.dll module to detect at runtime; module-name heuristics
// are useless here. Poll for a visible main window (engine init) instead, and
// proceed after the timeout with a clear log line rather than blocking forever.
static void WaitForTargetReady(HANDLE hProcess)
{
    const DWORD kTimeoutMs = 30000;
    const DWORD kPollMs = 250;
    const DWORD pid = GetProcessId(hProcess);

    DWORD elapsed = 0;
    while (elapsed < kTimeoutMs)
    {
        if (TargetHasMainWindow(pid))
        {
            return;
        }
        Sleep(kPollMs);
        elapsed += kPollMs;
    }

    std::wcout << L"[*] Engine-readiness wait timed out after " << (kTimeoutMs / 1000)
               << L"s (no main window observed yet); proceeding with injection anyway.\n";
    OutputDebugStringW(L"[Injector] Engine-readiness gate timed out; proceeding.\n");
}

// ---------------------------------------------------------------------------
// Injection: Win32 CreateRemoteThread + LoadLibraryW
// ---------------------------------------------------------------------------
static bool InjectDLL(HANDLE hProcess, const std::wstring& rawDllPath)
{
    std::wstring dllPath = GetFullPath(rawDllPath);

    if (!FileExists(dllPath))
    {
        std::wcerr << L"[!] Target DLL not found: " << dllPath << L"\n";
        return false;
    }

    EnableDebugPrivilege();

    // (a) Upfront bitness check: fail fast on x64-DLL-into-x86 (or vice versa).
    {
        DWORD targetPid = GetProcessId(hProcess);
        if (!CheckBitnessCompatibility(hProcess, targetPid))
        {
            return false;
        }
    }

    // (b) Stage the DLL next to the target exe so ffi.load("ZoneClient")
    // resolves it via the LoadLibrary exe-directory search.
    dllPath = DeployDllBesideTarget(hProcess, dllPath);

    // (c) Readiness gate: bounded poll for a main window instead of a fixed
    // Sleep. Runs before any remote allocation/thread.
    WaitForTargetReady(hProcess);

    const size_t pathBytes = (dllPath.length() + 1) * sizeof(wchar_t);
    void* pRemoteBuf = VirtualAllocEx(hProcess, nullptr, pathBytes,
                                      MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    if (!pRemoteBuf)
    {
        std::wcerr << L"[!] VirtualAllocEx failed (error " << GetLastError() << L")\n";
        return false;
    }

    if (!WriteProcessMemory(hProcess, pRemoteBuf, dllPath.c_str(), pathBytes, nullptr))
    {
        std::wcerr << L"[!] WriteProcessMemory failed (error " << GetLastError() << L")\n";
        VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);
        return false;
    }

    HMODULE hKernel32 = GetModuleHandleW(L"kernel32.dll");
    if (!hKernel32)
    {
        std::wcerr << L"[!] GetModuleHandleW(kernel32.dll) failed\n";
        VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);
        return false;
    }

    FARPROC pLoadLibraryW = GetProcAddress(hKernel32, "LoadLibraryW");
    if (!pLoadLibraryW)
    {
        std::wcerr << L"[!] GetProcAddress(LoadLibraryW) failed\n";
        VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);
        return false;
    }

    HANDLE hThread = CreateRemoteThread(
        hProcess,
        nullptr,
        0,
        reinterpret_cast<LPTHREAD_START_ROUTINE>(pLoadLibraryW),
        pRemoteBuf,
        0,
        nullptr
    );

    if (!hThread)
    {
        std::wcerr << L"[!] CreateRemoteThread failed (error " << GetLastError() << L")\n";
        VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);
        return false;
    }

    std::wcout << L"[*] Remote thread started, awaiting LoadLibraryW completion...\n";
    DWORD waitResult = WaitForSingleObject(hThread, 15000);
    if (waitResult == WAIT_TIMEOUT)
    {
        std::wcerr << L"[!] Remote thread execution timed out.\n";
        // C-14: Do NOT call VirtualFreeEx on timeout because the remote thread
        // may still be running and dereferencing pRemoteBuf.
        CloseHandle(hThread);
        return false;
    }
    else if (waitResult != WAIT_OBJECT_0)
    {
        std::wcerr << L"[!] WaitForSingleObject failed (error " << GetLastError() << L")\n";
        CloseHandle(hThread);
        return false;
    }

    DWORD exitCode = 0;
    GetExitCodeThread(hThread, &exitCode);
    CloseHandle(hThread);

    // C-14: Only free remote buffer if WaitForSingleObject returned WAIT_OBJECT_0
    VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);

    if (exitCode == 0)
    {
        std::wcerr << L"[!] Remote LoadLibraryW returned NULL (exit code 0).\n"
                   << L"    Check DLL dependencies and bitness (must match 64-bit target process).\n";
        return false;
    }

    return true;
}

static bool InjectDLL(DWORD processID, const std::wstring& rawDllPath)
{
    EnableDebugPrivilege();

    HANDLE hProcess = OpenProcess(
        PROCESS_CREATE_THREAD | PROCESS_QUERY_INFORMATION | PROCESS_VM_OPERATION | PROCESS_VM_WRITE | PROCESS_VM_READ,
        FALSE,
        processID
    );
    if (!hProcess)
    {
        hProcess = OpenProcess(PROCESS_ALL_ACCESS, FALSE, processID);
    }
    if (!hProcess)
    {
        std::wcerr << L"[!] OpenProcess failed for PID " << processID
                   << L" (error " << GetLastError() << L")\n";
        return false;
    }

    bool result = InjectDLL(hProcess, rawDllPath);
    CloseHandle(hProcess);
    return result;
}

// ---------------------------------------------------------------------------
// Mode: --wait (poll for known Anomaly executables)
// ---------------------------------------------------------------------------
static int ModeWait(const std::wstring& dllPath)
{
    const std::vector<std::wstring> targetExes = {
        L"AnomalyDX11.exe",
        L"AnomalyDX11AVX.exe",
        L"AnomalyDX10.exe",
        L"AnomalyDX10AVX.exe",
        L"AnomalyDX9.exe",
        L"AnomalyDX9AVX.exe",
        L"AnomalyDX8.exe",
        L"AnomalyDX8AVX.exe",
        L"xrEngine.exe"
    };

    std::wcout << L"[*] Mode: WAIT - polling for Anomaly processes...\n";
    std::wcout << L"    Monitored targets: AnomalyDX11.exe, AnomalyDX11AVX.exe, AnomalyDX10.exe, "
                  L"AnomalyDX10AVX.exe, AnomalyDX9.exe, AnomalyDX9AVX.exe, AnomalyDX8.exe, "
                  L"AnomalyDX8AVX.exe, xrEngine.exe\n";
    std::wcout << L"    Press Ctrl+C to cancel.\n\n";

    DWORD pid = 0;
    std::wstring foundExe;

    while (pid == 0)
    {
        for (const auto& exe : targetExes)
        {
            pid = FindProcessId(exe);
            if (pid != 0)
            {
                foundExe = exe;
                break;
            }
        }
        if (pid == 0)
        {
            Sleep(500);
        }
    }

    std::wcout << L"[+] Detected target process: " << foundExe << L" (PID: " << pid << L")\n";
    std::wcout << L"[*] Waiting for engine readiness (bounded gate inside InjectDLL)...\n";
    // No fixed Sleep here; InjectDLL polls for the main window.

    std::wcout << L"[*] Injecting " << dllPath << L" into PID " << pid << L"...\n";
    if (InjectDLL(pid, dllPath))
    {
        std::wcout << L"[+] Injection successful!\n";
        return 0;
    }

    std::wcerr << L"[!] Injection failed!\n";
    return 1;
}

// ---------------------------------------------------------------------------
// Mode: --pid <pid> (inject directly into specified PID)
// ---------------------------------------------------------------------------
static int ModePid(DWORD pid, const std::wstring& dllPath)
{
    std::wcout << L"[*] Mode: PID " << pid << L"\n";
    std::wcout << L"[*] Injecting " << dllPath << L" into PID " << pid << L"...\n";
    if (InjectDLL(pid, dllPath))
    {
        std::wcout << L"[+] Injection successful!\n";
        return 0;
    }

    std::wcerr << L"[!] Injection failed!\n";
    return 1;
}

// ---------------------------------------------------------------------------
// Mode: --launch <exe_path> [args...] (spawn game suspended, resume, wait, inject)
// ---------------------------------------------------------------------------
static int ModeLaunch(const std::wstring& rawExePath,
                      const std::vector<std::wstring>& extraArgs,
                      const std::wstring& dllPath,
                      bool ephemeral)
{
    std::wstring exePath = GetFullPath(rawExePath);
    if (!FileExists(exePath))
    {
        std::wcerr << L"[!] Target executable not found: " << exePath << L"\n";
        return 1;
    }

    // Determine appropriate working directory for Anomaly
    std::wstring workDir;
    size_t lastSlash = exePath.find_last_of(L"\\/");
    if (lastSlash != std::wstring::npos)
    {
        workDir = exePath.substr(0, lastSlash);
        // Check if parent contains fsgame.ltx (standard Anomaly root layout: <root>/bin/<exe>)
        size_t parentSlash = workDir.find_last_of(L"\\/");
        if (parentSlash != std::wstring::npos)
        {
            std::wstring candidateRoot = workDir.substr(0, parentSlash);
            if (FileExists(candidateRoot + L"\\fsgame.ltx"))
            {
                workDir = candidateRoot;
            }
        }
    }

    {
        wchar_t injPath[MAX_PATH] = {};
        GetModuleFileNameW(nullptr, injPath, MAX_PATH);
        std::wstring injDir(injPath);
        size_t injSlash = injDir.find_last_of(L"\\/");
        if (injSlash != std::wstring::npos)
        {
            injDir = injDir.substr(0, injSlash);
        }
        PreProvisionAssets(workDir, injDir);
    }

    // Build command line string: "<exe>" [args...]
    std::wstring cmdLine = L"\"" + exePath + L"\"";
    for (const auto& arg : extraArgs)
    {
        cmdLine += L" ";
        if (arg.find(L' ') != std::wstring::npos && arg.front() != L'\"')
        {
            cmdLine += L"\"" + arg + L"\"";
        }
        else
        {
            cmdLine += arg;
        }
    }

    std::wcout << L"[*] Mode: LAUNCH\n";
    std::wcout << L"[*] Executable: " << exePath << L"\n";
    if (!workDir.empty())
    {
        std::wcout << L"[*] Working Dir: " << workDir << L"\n";
    }
    std::wcout << L"[*] Command line: " << cmdLine << L"\n\n";

    STARTUPINFOW si = {};
    si.cb = sizeof(si);
    PROCESS_INFORMATION pi = {};

    std::vector<wchar_t> cmdLineBuf(cmdLine.begin(), cmdLine.end());
    cmdLineBuf.push_back(L'\0');

    if (!CreateProcessW(
            exePath.c_str(),
            cmdLineBuf.data(),
            nullptr,
            nullptr,
            FALSE,
            CREATE_SUSPENDED,
            nullptr,
            workDir.empty() ? nullptr : workDir.c_str(),
            &si,
            &pi))
    {
        std::wcerr << L"[!] CreateProcess failed (error " << GetLastError() << L")\n";
        return 1;
    }

    std::wcout << L"[+] Process created (PID: " << pi.dwProcessId << L"), resuming main thread...\n";
    ResumeThread(pi.hThread);

    std::wcout << L"[*] Waiting for game engine initialization...\n";
    WaitForInputIdle(pi.hProcess, 5000); // best-effort precursor only; bounded readiness gate runs inside InjectDLL
    // No fixed Sleep(2000) here; InjectDLL polls for the main window.

    // Verify process is still alive before attempting injection
    DWORD procExitCode = 0;
    if (GetExitCodeProcess(pi.hProcess, &procExitCode) && procExitCode != STILL_ACTIVE)
    {
        std::wcerr << L"[!] Target process terminated prematurely (exit code " << procExitCode << L")\n";
        CloseHandle(pi.hThread);
        CloseHandle(pi.hProcess);
        return 1;
    }

    // C-26: Keep pi.hProcess and pi.hThread open and pass pi.hProcess directly to eliminate TOCTOU PID reuse race
    std::wcout << L"[*] Injecting " << dllPath << L" into PID " << pi.dwProcessId << L"...\n";
    bool injected = InjectDLL(pi.hProcess, dllPath);
    if (injected && ephemeral)
    {
        std::wcout << L"[*] Ephemeral mode: waiting for game exit...\n";
        DWORD wr = WaitForSingleObject(pi.hProcess, INFINITE);
        if (wr != WAIT_OBJECT_0)
        {
            std::wcerr << L"[!] Wait for game exit failed (error " << GetLastError() << L")\n";
        }
        int purgeRc = PurgeProvisionedFiles(workDir);
        CloseHandle(pi.hThread);
        CloseHandle(pi.hProcess);
        std::wcout << L"[+] Ephemeral session ended - game files purged\n";
        if (purgeRc != 0)
        {
            return 1;
        }
        return 0;
    }
    CloseHandle(pi.hThread);
    CloseHandle(pi.hProcess);

    if (injected)
    {
        std::wcout << L"[+] Injection successful!\n";
        return 0;
    }

    std::wcerr << L"[!] Injection failed!\n";
    return 1;
}

// ---------------------------------------------------------------------------
// Mode: --cleanup <game_exe_or_root> (purge pre-provisioned zone files)
// ---------------------------------------------------------------------------
static int ModeCleanup(const std::wstring& rawPath)
{
    std::wstring full = GetFullPath(rawPath);
    while (full.size() > 3 && (full.back() == L'\\' || full.back() == L'/'))
    {
        full.pop_back();
    }
    std::wstring gameRoot;
    DWORD attr = GetFileAttributesW(full.c_str());
    bool isDir = (attr != INVALID_FILE_ATTRIBUTES && (attr & FILE_ATTRIBUTE_DIRECTORY));
    if (isDir)
    {
        gameRoot = full;
        if (!FileExists(gameRoot + L"\\fsgame.ltx"))
        {
            size_t slash = gameRoot.find_last_of(L"\\/");
            if (slash != std::wstring::npos)
            {
                std::wstring parent = gameRoot.substr(0, slash);
                if (FileExists(parent + L"\\fsgame.ltx"))
                {
                    gameRoot = parent;
                }
            }
        }
    }
    else
    {
        size_t lastSlash = full.find_last_of(L"\\/");
        if (lastSlash != std::wstring::npos)
        {
            gameRoot = full.substr(0, lastSlash);
        }
        else
        {
            gameRoot = full;
        }
        size_t parentSlash = gameRoot.find_last_of(L"\\/");
        if (parentSlash != std::wstring::npos)
        {
            std::wstring candidateRoot = gameRoot.substr(0, parentSlash);
            if (FileExists(candidateRoot + L"\\fsgame.ltx"))
            {
                gameRoot = candidateRoot;
            }
        }
    }
    if (gameRoot.empty())
    {
        std::wcerr << L"[!] Cleanup failed: unable to resolve game root.\n";
        return 1;
    }
    std::wcout << L"[*] Mode: CLEANUP\n";
    std::wcout << L"[*] Game Root: " << gameRoot << L"\n";
    return PurgeProvisionedFiles(gameRoot);
}

// ---------------------------------------------------------------------------
// Usage Display
// ---------------------------------------------------------------------------
static void ShowUsage(const wchar_t* exeName)
{
    std::wcout << L"Usage: " << exeName << L" [options]\n\n"
               << L"Modes:\n"
               << L"  --wait                     (Default) Poll for running Anomaly processes and inject\n"
               << L"  --launch <exe> [args...]   Spawn game process (CREATE_SUSPENDED -> Resume) and inject\n"
               << L"  --pid <pid>                Inject directly into specified process ID\n"
               << L"  --cleanup <exe_or_root>    Purge pre-provisioned zone files from game root\n\n"
               << L"Options:\n"
               << L"  --dll <path>               Custom path to ZoneClient.dll (default: ZoneClient.dll next to injector)\n"
               << L"  --ephemeral                With --launch: wait for game exit then purge zone files\n"
               << L"  --help, -h                 Display this help message\n\n"
               << L"Examples:\n"
               << L"  ZoneClient_Injector.exe --wait\n"
               << L"  ZoneClient_Injector.exe --launch \"C:\\Anomaly\\bin\\AnomalyDX11.exe\" -dbg -smap4096\n"
               << L"  ZoneClient_Injector.exe --launch \"C:\\Anomaly\\bin\\AnomalyDX11.exe\" --ephemeral\n"
               << L"  ZoneClient_Injector.exe --cleanup \"C:\\Anomaly\\bin\\AnomalyDX11.exe\"\n"
               << L"  ZoneClient_Injector.exe --cleanup \"C:\\Anomaly\"\n"
               << L"  ZoneClient_Injector.exe --pid 12345\n"
               << L"  ZoneClient_Injector.exe --dll \"C:\\Path\\To\\ZoneClient.dll\" --wait\n";
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------
int main(int argc, char** argv)
{
    std::wcout << L"=======================================\n";
    std::wcout << L"   Zone Injector v1.0           \n";
    std::wcout << L"=======================================\n\n";

    // Convert arguments to wide strings
    std::vector<std::wstring> args;
    args.reserve(static_cast<size_t>(argc));
    for (int i = 0; i < argc; ++i)
    {
        int needed = MultiByteToWideChar(CP_ACP, 0, argv[i], -1, nullptr, 0);
        std::wstring ws(static_cast<size_t>(needed), L'\0');
        MultiByteToWideChar(CP_ACP, 0, argv[i], -1, &ws[0], needed);
        if (!ws.empty() && ws.back() == L'\0')
        {
            ws.pop_back();
        }
        args.push_back(std::move(ws));
    }

    const wchar_t* progName = args.empty() ? L"ZoneClient_Injector.exe" : args[0].c_str();

    // Check for help flag anywhere
    for (size_t i = 1; i < args.size(); ++i)
    {
        if (args[i] == L"--help" || args[i] == L"-h" || args[i] == L"/?")
        {
            ShowUsage(progName);
            return 0;
        }
    }

    // Extract --dll override if present
    std::wstring dllPath = DefaultDllPath();
    for (size_t i = 1; i + 1 < args.size(); ++i)
    {
        if (args[i] == L"--dll")
        {
            dllPath = GetFullPath(args[i + 1]);
            args.erase(args.begin() + static_cast<ptrdiff_t>(i),
                       args.begin() + static_cast<ptrdiff_t>(i) + 2);
            break;
        }
    }

    std::wcout << L"[*] DLL Target: " << dllPath << L"\n\n";

    bool ephemeral = false;
    for (size_t i = 1; i < args.size();)
    {
        if (args[i] == L"--ephemeral")
        {
            ephemeral = true;
            args.erase(args.begin() + static_cast<ptrdiff_t>(i));
        }
        else
        {
            ++i;
        }
    }

    if (ephemeral && (args.size() < 2 || args[1] != L"--launch"))
    {
        std::wcerr << L"[!] Error: --ephemeral requires --launch.\n\n";
        ShowUsage(progName);
        return 1;
    }

    // Determine Mode
    if (args.size() >= 2 && args[1] == L"--cleanup")
    {
        if (args.size() < 3)
        {
            std::wcerr << L"[!] Error: --cleanup requires a game exe or root argument.\n\n";
            ShowUsage(progName);
            return 1;
        }
        return ModeCleanup(args[2]);
    }

    if (args.size() >= 2 && args[1] == L"--pid")
    {
        if (args.size() < 3)
        {
            std::wcerr << L"[!] Error: --pid requires a target process ID argument.\n\n";
            ShowUsage(progName);
            return 1;
        }

        DWORD pid = 0;
        try
        {
            size_t idx = 0;
            int base = (args[2].rfind(L"0x", 0) == 0 || args[2].rfind(L"0X", 0) == 0) ? 16 : 10;
            pid = static_cast<DWORD>(std::stoul(args[2], &idx, base));
        }
        catch (...)
        {
            pid = 0;
        }

        if (pid == 0)
        {
            std::wcerr << L"[!] Error: Invalid PID specified: " << args[2] << L"\n";
            return 1;
        }

        return ModePid(pid, dllPath);
    }

    if (args.size() >= 2 && args[1] == L"--launch")
    {
        if (args.size() < 3)
        {
            std::wcerr << L"[!] Error: --launch requires an executable path argument.\n\n";
            ShowUsage(progName);
            return 1;
        }

        std::wstring exePath = args[2];
        std::vector<std::wstring> extraArgs;
        for (size_t i = 3; i < args.size(); ++i)
        {
            extraArgs.push_back(args[i]);
        }

        return ModeLaunch(exePath, extraArgs, dllPath, ephemeral);
    }

    if (args.size() >= 2 && args[1] != L"--wait")
    {
        std::wcerr << L"[!] Unknown option: " << args[1] << L"\n\n";
        ShowUsage(progName);
        return 1;
    }

    // Default mode: --wait
    return ModeWait(dllPath);
}