#include <windows.h>
#include <exception>
#include "provision/asset_provisioner.h"
#include "identity/identity.h"
#include "net/udp_client.h"
#include <MinHook.h>

namespace
{
    HMODULE g_hModule = nullptr;

    DWORD WINAPI InitThread(LPVOID /*lpParam*/)
    {
        try
        {
            // Provision embedded assets before any delay: the engine finishes its
            // gamedata filesystem scan ~0.5s after process start, and files written
            // after the scan stay invisible for the whole session.
            AssetProvisioner::EnsureAssets();

            Sleep(100);

            MH_STATUS mhStatus = MH_Initialize();
            if (mhStatus != MH_OK && mhStatus != MH_ERROR_ALREADY_INITIALIZED)
            {
                OutputDebugStringA("[ZoneClient] Failed to initialize MinHook!\n");
            }

            Identity::Init();
            NetClient::Init();
        }
        catch (const std::exception& e)
        {
            OutputDebugStringA("[ZoneClient] Exception in InitThread: ");
            OutputDebugStringA(e.what());
            OutputDebugStringA("\n");
        }
        catch (...)
        {
            OutputDebugStringA("[ZoneClient] Unknown exception in InitThread!\n");
        }
        return 0;
    }
}

BOOL WINAPI DllMain(HINSTANCE hinstDLL, DWORD fdwReason, LPVOID lpvReserved)
{
    switch (fdwReason)
    {
        case DLL_PROCESS_ATTACH:
        {
            g_hModule = hinstDLL;
            DisableThreadLibraryCalls(hinstDLL);

            HANDLE hThread = CreateThread(nullptr, 0, InitThread, nullptr, 0, nullptr);
            if (hThread)
            {
                CloseHandle(hThread);
            }
            break;
        }

        case DLL_PROCESS_DETACH:
        {
            // PRODUCTION FIX: never join() the net thread or MH_Uninitialize()
            // under the loader lock (FreeLibrary path deadlocks when the worker
            // is in select/sendto/CRT). Signal shutdown only; the worker is
            // daemonized and the OS reclaims the socket on process exit.
            // Previous code called NetClient::Shutdown() here (join + WSACleanup).
            // On process termination (lpvReserved != nullptr) skip a best-effort
            // DISCONNECT so the server drops the session promptly; FreeLibrary
            // still performs no cleanup at all.
            if (lpvReserved != nullptr)
            {
                NetClient::SendDisconnectBestEffort();
            }
            break;
        }

        default:
            break;
    }

    return TRUE;
}