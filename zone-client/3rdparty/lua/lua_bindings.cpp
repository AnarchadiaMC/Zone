#include "lua.h"
#include "lauxlib.h"
#include "lualib.h"
#include <windows.h>
#include <cstdint>
#include <cstddef>

// Define function pointers declared in lua.h
pfn_lua_pushnumber p_lua_pushnumber = nullptr;
pfn_lua_pushstring p_lua_pushstring = nullptr;
pfn_lua_pushcclosure p_lua_pushcclosure = nullptr;
pfn_lua_setfield p_lua_setfield = nullptr;
pfn_lua_newtable p_lua_newtable = nullptr;
pfn_lua_tonumber p_lua_tonumber = nullptr;
pfn_lua_tostring p_lua_tostring = nullptr;
pfn_lua_toboolean p_lua_toboolean = nullptr;
pfn_lua_gettop p_lua_gettop = nullptr;
pfn_lua_pushboolean p_lua_pushboolean = nullptr;
pfn_lua_pushinteger p_lua_pushinteger = nullptr;
pfn_lua_pushlightuserdata p_lua_pushlightuserdata = nullptr;
pfn_lua_setglobal p_lua_setglobal = nullptr;
pfn_lua_pushnil p_lua_pushnil = nullptr;
pfn_lua_pushlstring p_lua_pushlstring = nullptr;
pfn_luaL_checkstring p_luaL_checkstring = nullptr;
pfn_luaL_checknumber p_luaL_checknumber = nullptr;

extern "C" {
    void ZN_Connect(const char* ip, double port, const char* uuid, double hwid, const char* nick);
    void ZN_Disconnect();
    void ZN_SendTransform(float x, float y, float z, int16_t yaw, int16_t pitch, int16_t vx, int16_t vy, int16_t vz, uint8_t animflags);
    bool ZN_PollEvent(char* buf, size_t* len);
    bool ZN_IsInSafeZone();
    bool ZN_IsConnected();
    void ZN_GetSessionInfo(uint32_t* sessionID, uint32_t* ping);
    void ZN_SendChatText(const char* text);
}

static bool ResolveLuaSymbols()
{
    HMODULE hLua = GetModuleHandleA("LuaJIT.dll");
    if (!hLua) hLua = GetModuleHandleA("lua51.dll");
    if (!hLua) return false;

    p_lua_pushnumber = (pfn_lua_pushnumber)GetProcAddress(hLua, "lua_pushnumber");
    p_lua_pushstring = (pfn_lua_pushstring)GetProcAddress(hLua, "lua_pushstring");
    p_lua_pushcclosure = (pfn_lua_pushcclosure)GetProcAddress(hLua, "lua_pushcclosure");
    p_lua_setfield = (pfn_lua_setfield)GetProcAddress(hLua, "lua_setfield");
    p_lua_newtable = (pfn_lua_newtable)GetProcAddress(hLua, "lua_newtable");
    p_lua_tonumber = (pfn_lua_tonumber)GetProcAddress(hLua, "lua_tonumber");
    p_lua_tostring = (pfn_lua_tostring)GetProcAddress(hLua, "lua_tostring");
    p_lua_toboolean = (pfn_lua_toboolean)GetProcAddress(hLua, "lua_toboolean");
    p_lua_gettop = (pfn_lua_gettop)GetProcAddress(hLua, "lua_gettop");
    p_lua_pushboolean = (pfn_lua_pushboolean)GetProcAddress(hLua, "lua_pushboolean");
    p_lua_pushinteger = (pfn_lua_pushinteger)GetProcAddress(hLua, "lua_pushinteger");
    p_lua_pushlightuserdata = (pfn_lua_pushlightuserdata)GetProcAddress(hLua, "lua_pushlightuserdata");
    p_lua_setglobal = (pfn_lua_setglobal)GetProcAddress(hLua, "lua_setglobal");
    p_lua_pushnil = (pfn_lua_pushnil)GetProcAddress(hLua, "lua_pushnil");
    p_lua_pushlstring = (pfn_lua_pushlstring)GetProcAddress(hLua, "lua_pushlstring");
    p_luaL_checkstring = (pfn_luaL_checkstring)GetProcAddress(hLua, "luaL_checkstring");
    p_luaL_checknumber = (pfn_luaL_checknumber)GetProcAddress(hLua, "luaL_checknumber");

    return p_lua_pushnumber != nullptr;
}

namespace
{
    int Lua_ZN_Connect(lua_State* L)
    {
        const char* ip = luaL_checkstring(L, 1);
        double port = luaL_checknumber(L, 2);
        const char* uuid = luaL_checkstring(L, 3);
        double hwid = luaL_checknumber(L, 4);
        const char* nick = luaL_checkstring(L, 5);
        ZN_Connect(ip, port, uuid, hwid, nick);
        return 0;
    }

    int Lua_ZN_Disconnect(lua_State* L)
    {
        ZN_Disconnect();
        return 0;
    }

    int Lua_ZN_SendTransform(lua_State* L)
    {
        float x = (float)luaL_checknumber(L, 1);
        float y = (float)luaL_checknumber(L, 2);
        float z = (float)luaL_checknumber(L, 3);
        int16_t yaw = (int16_t)luaL_checknumber(L, 4);
        int16_t pitch = (int16_t)luaL_checknumber(L, 5);
        int16_t vx = (int16_t)luaL_checknumber(L, 6);
        int16_t vy = (int16_t)luaL_checknumber(L, 7);
        int16_t vz = (int16_t)luaL_checknumber(L, 8);
        uint8_t animflags = (uint8_t)luaL_checknumber(L, 9);
        ZN_SendTransform(x, y, z, yaw, pitch, vx, vy, vz, animflags);
        return 0;
    }

    int Lua_ZN_PollEvent(lua_State* L)
    {
        char buf[1500];
        size_t len = sizeof(buf);
        if (ZN_PollEvent(buf, &len))
        {
            lua_pushlstring(L, buf, len);
            return 1;
        }
        lua_pushnil(L);
        return 1;
    }

    int Lua_ZN_IsInSafeZone(lua_State* L)
    {
        lua_pushboolean(L, ZN_IsInSafeZone() ? 1 : 0);
        return 1;
    }

    int Lua_ZN_IsConnected(lua_State* L)
    {
        lua_pushboolean(L, ZN_IsConnected() ? 1 : 0);
        return 1;
    }

    int Lua_ZN_GetSessionInfo(lua_State* L)
    {
        uint32_t sessionID = 0;
        uint32_t ping = 0;
        ZN_GetSessionInfo(&sessionID, &ping);
        lua_pushnumber(L, (double)sessionID);
        lua_pushnumber(L, (double)ping);
        return 2;
    }

    int Lua_ZN_SendChatText(lua_State* L)
    {
        const char* text = luaL_checkstring(L, 1);
        ZN_SendChatText(text ? text : "");
        return 0;
    }
}

namespace ZoneBindings
{
    void RegisterZoneNetBindings(lua_State* L)
    {
        if (!ResolveLuaSymbols())
            return;

        lua_newtable(L);
        
        lua_pushcclosure(L, Lua_ZN_Connect, 0); lua_setfield(L, -2, "Connect");
        lua_pushcclosure(L, Lua_ZN_Disconnect, 0); lua_setfield(L, -2, "Disconnect");
        lua_pushcclosure(L, Lua_ZN_SendTransform, 0); lua_setfield(L, -2, "SendTransform");
        lua_pushcclosure(L, Lua_ZN_PollEvent, 0); lua_setfield(L, -2, "PollEvent");
        lua_pushcclosure(L, Lua_ZN_IsInSafeZone, 0); lua_setfield(L, -2, "IsInSafeZone");
        lua_pushcclosure(L, Lua_ZN_IsConnected, 0); lua_setfield(L, -2, "IsConnected");
        lua_pushcclosure(L, Lua_ZN_GetSessionInfo, 0); lua_setfield(L, -2, "GetSessionInfo");
        lua_pushcclosure(L, Lua_ZN_SendChatText, 0); lua_setfield(L, -2, "SendChatText");
        
        lua_setglobal(L, "ZoneNet");
    }
}
