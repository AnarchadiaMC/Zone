#include <windows.h>
#include "provision/asset_provisioner.h"
#include "identity/identity.h"
#include "net/udp_client.h"
#include "hook/lua_hook.h"
#include <MinHook.h>

namespace
{
    HMODULE g_hModule = nullptr;

    DWORD WINAPI InitThread(LPVOID /*lpParam*/)
    {
        Sleep(500);
        NetClient::Init();
        LuaHook::Install();
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

            MH_Initialize();
            Identity::Init();
            AssetProvisioner::EnsureAssets();

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
                LuaHook::Uninstall();
                MH_Uninitialize();
            }
            break;
        }

        default:
            break;
    }

    return TRUE;
}