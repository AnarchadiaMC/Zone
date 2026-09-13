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

namespace LuaHook
{
    void Install()
    {
        HMODULE hLua = GetModuleHandleA("LuaJIT.dll");
        if (hLua)
        {
            void* pTarget = (void*)GetProcAddress(hLua, "luaL_openlibs");
            if (pTarget)
            {
                MH_CreateHook(pTarget, (void*)&Hooked_luaL_openlibs, (LPVOID*)&g_Orig_luaL_openlibs);
                MH_EnableHook(pTarget);
            }
        }
    }

    void Uninstall()
    {
        HMODULE hLua = GetModuleHandleA("LuaJIT.dll");
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
