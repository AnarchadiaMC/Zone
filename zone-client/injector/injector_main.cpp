#include <windows.h>
#include <tlhelp32.h>
#include <psapi.h>
#include <iostream>
#include <string>
#include <vector>
#include <algorithm>

#pragma comment(lib, "advapi32.lib")

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
// Injection: Win32 CreateRemoteThread + LoadLibraryW
// ---------------------------------------------------------------------------
static bool InjectDLL(DWORD processID, const std::wstring& rawDllPath)
{
    std::wstring dllPath = GetFullPath(rawDllPath);

    if (!FileExists(dllPath))
    {
        std::wcerr << L"[!] Target DLL not found: " << dllPath << L"\n";
        return false;
    }

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

    const size_t pathBytes = (dllPath.length() + 1) * sizeof(wchar_t);
    void* pRemoteBuf = VirtualAllocEx(hProcess, nullptr, pathBytes,
                                      MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    if (!pRemoteBuf)
    {
        std::wcerr << L"[!] VirtualAllocEx failed (error " << GetLastError() << L")\n";
        CloseHandle(hProcess);
        return false;
    }

    if (!WriteProcessMemory(hProcess, pRemoteBuf, dllPath.c_str(), pathBytes, nullptr))
    {
        std::wcerr << L"[!] WriteProcessMemory failed (error " << GetLastError() << L")\n";
        VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);
        CloseHandle(hProcess);
        return false;
    }

    HMODULE hKernel32 = GetModuleHandleW(L"kernel32.dll");
    if (!hKernel32)
    {
        std::wcerr << L"[!] GetModuleHandleW(kernel32.dll) failed\n";
        VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);
        CloseHandle(hProcess);
        return false;
    }

    FARPROC pLoadLibraryW = GetProcAddress(hKernel32, "LoadLibraryW");
    if (!pLoadLibraryW)
    {
        std::wcerr << L"[!] GetProcAddress(LoadLibraryW) failed\n";
        VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);
        CloseHandle(hProcess);
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
        CloseHandle(hProcess);
        return false;
    }

    std::wcout << L"[*] Remote thread started, awaiting LoadLibraryW completion...\n";
    DWORD waitResult = WaitForSingleObject(hThread, 15000);
    if (waitResult == WAIT_TIMEOUT)
    {
        std::wcerr << L"[!] Remote thread execution timed out.\n";
    }

    DWORD exitCode = 0;
    GetExitCodeThread(hThread, &exitCode);
    CloseHandle(hThread);

    VirtualFreeEx(hProcess, pRemoteBuf, 0, MEM_RELEASE);
    CloseHandle(hProcess);

    if (exitCode == 0)
    {
        std::wcerr << L"[!] Remote LoadLibraryW returned NULL (exit code 0).\n"
                   << L"    Check DLL dependencies and bitness (must match 64-bit target process).\n";
        return false;
    }

    return true;
}

// ---------------------------------------------------------------------------
// Mode: --wait (poll for known Anomaly executables)
// ---------------------------------------------------------------------------
static int ModeWait(const std::wstring& dllPath)
{
    const std::vector<std::wstring> targetExes = {
        L"AnomalyDX11.exe",
        L"AnomalyDX11AVX.exe",
        L"VerifiedDX11.exe",
        L"xrEngine.exe"
    };

    std::wcout << L"[*] Mode: WAIT - polling for Anomaly processes...\n";
    std::wcout << L"    Monitored targets: AnomalyDX11.exe, AnomalyDX11AVX.exe, VerifiedDX11.exe, xrEngine.exe\n";
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
    std::wcout << L"[*] Waiting 1.5s for process initialization...\n";
    Sleep(1500);

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
                      const std::wstring& dllPath)
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
    CloseHandle(pi.hThread);

    std::wcout << L"[*] Waiting for game engine initialization...\n";
    WaitForInputIdle(pi.hProcess, 5000);
    Sleep(2000);

    // Verify process is still alive before attempting injection
    DWORD procExitCode = 0;
    if (GetExitCodeProcess(pi.hProcess, &procExitCode) && procExitCode != STILL_ACTIVE)
    {
        std::wcerr << L"[!] Target process terminated prematurely (exit code " << procExitCode << L")\n";
        CloseHandle(pi.hProcess);
        return 1;
    }
    CloseHandle(pi.hProcess);

    std::wcout << L"[*] Injecting " << dllPath << L" into PID " << pi.dwProcessId << L"...\n";
    if (InjectDLL(pi.dwProcessId, dllPath))
    {
        std::wcout << L"[+] Injection successful!\n";
        return 0;
    }

    std::wcerr << L"[!] Injection failed!\n";
    return 1;
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
               << L"  --pid <pid>                Inject directly into specified process ID\n\n"
               << L"Options:\n"
               << L"  --dll <path>               Custom path to ZoneClient.dll (default: ZoneClient.dll next to injector)\n"
               << L"  --help, -h                 Display this help message\n\n"
               << L"Examples:\n"
               << L"  ZoneClient_Injector.exe --wait\n"
               << L"  ZoneClient_Injector.exe --launch \"C:\\Anomaly\\bin\\AnomalyDX11.exe\" -dbg -smap4096\n"
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

    // Determine Mode
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

        return ModeLaunch(exePath, extraArgs, dllPath);
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