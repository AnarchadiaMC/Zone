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
            Sleep(500);

            MH_STATUS mhStatus = MH_Initialize();
            if (mhStatus != MH_OK && mhStatus != MH_ERROR_ALREADY_INITIALIZED)
            {
                OutputDebugStringA("[ZoneClient] Failed to initialize MinHook!\n");
            }

            Identity::Init();
            AssetProvisioner::EnsureAssets();
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
            // lpvReserved == nullptr means FreeLibrary; skip cleanup on process exit
            if (!lpvReserved)
            {
                NetClient::Shutdown();
                MH_Uninitialize();
            }
            break;
        }

        default:
            break;
    }

    return TRUE;
}