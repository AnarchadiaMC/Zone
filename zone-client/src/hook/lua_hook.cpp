#include "lua_hook.h"
#include "../lua/zone_bindings.h"
#include <MinHook.h>
#include <windows.h>
#include "lauxlib.h"
#include "lualib.h"

namespace
{
    typedef void (*luaL_openlibs_t)(lua_State *L);
    luaL_openlibs_t g_Orig_luaL_openlibs = nullptr;

    void Hooked_luaL_openlibs(lua_State *L)
    {
        if (g_Orig_luaL_openlibs)
            g_Orig_luaL_openlibs(L);
            
        ZoneBindings::RegisterZoneNetBindings(L);
    }
}

// NOTE (production): the game (xray-monolith, LuaJIT 2.0.4) links LuaJIT
// STATICALLY into AnomalyDX*.exe (luajit.vcxproj StaticLibrary, USE_LUAJIT_ONE
// undefined, script_storage.cpp calls luaL_openlibs in-process). There is no
// LuaJIT.dll / lua51.dll module at runtime, so a MinHook detour on
// luaL_openlibs via GetModuleHandle is a deliberate no-op. The supported
// ZoneNet path is LuaJIT FFI (zone_net.script ffi.load("ZoneClient") +
// ZN_* exports). This hook is retained only for forward-compat with a
// hypothetical DLL-based Lua build; it must never be required for Connect.
namespace LuaHook
{
    void Install()
    {
        HMODULE hLua = GetModuleHandleA("LuaJIT.dll");
        if (!hLua) hLua = GetModuleHandleA("lua51.dll");
        if (!hLua)
        {
            OutputDebugStringA("[ZoneClient] LuaHook::Install: no Lua DLL (static EXE) - FFI polyfill is primary, skipping hook.\n");
            return;
        }
        if (hLua)
        {
            void* pTarget = (void*)GetProcAddress(hLua, "luaL_openlibs");
            if (pTarget)
            {
                MH_STATUS createStatus = MH_CreateHook(pTarget, (void*)&Hooked_luaL_openlibs, (LPVOID*)&g_Orig_luaL_openlibs);
                if (createStatus != MH_OK)
                {
                    OutputDebugStringA("[ZoneClient] LuaHook::Install: MH_CreateHook failed!\n");
                    return;
                }

                MH_STATUS enableStatus = MH_EnableHook(pTarget);
                if (enableStatus != MH_OK)
                {
                    OutputDebugStringA("[ZoneClient] LuaHook::Install: MH_EnableHook failed!\n");
                    MH_RemoveHook(pTarget);
                    return;
                }
            }
        }
    }

    void Uninstall()
    {
        HMODULE hLua = GetModuleHandleA("LuaJIT.dll");
        if (!hLua) hLua = GetModuleHandleA("lua51.dll");
        if (hLua)
        {
            void* pTarget = (void*)GetProcAddress(hLua, "luaL_openlibs");
            if (pTarget)
            {
                MH_DisableHook(pTarget);
                MH_RemoveHook(pTarget);
            }
        }
    }
}
