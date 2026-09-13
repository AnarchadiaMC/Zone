#pragma once
#include <windows.h>
#include <stddef.h>

struct lua_State;
typedef int (*lua_CFunction) (struct lua_State *L);

typedef void (*pfn_lua_pushnumber)(struct lua_State *L, double n);
typedef void (*pfn_lua_pushstring)(struct lua_State *L, const char *s);
typedef void (*pfn_lua_pushcclosure)(struct lua_State *L, lua_CFunction fn, int n);
typedef void (*pfn_lua_setfield)(struct lua_State *L, int idx, const char *k);
typedef void (*pfn_lua_newtable)(struct lua_State *L);
typedef double (*pfn_lua_tonumber)(struct lua_State *L, int idx);
typedef const char* (*pfn_lua_tostring)(struct lua_State *L, int idx);
typedef int (*pfn_lua_toboolean)(struct lua_State *L, int idx);
typedef int (*pfn_lua_gettop)(struct lua_State *L);
typedef void (*pfn_lua_pushboolean)(struct lua_State *L, int b);
typedef void (*pfn_lua_pushinteger)(struct lua_State *L, ptrdiff_t n);
typedef void (*pfn_lua_pushlightuserdata)(struct lua_State *L, void *p);
typedef void (*pfn_lua_setglobal)(struct lua_State *L, const char *name);
typedef void (*pfn_lua_pushnil)(struct lua_State *L);
typedef void (*pfn_lua_pushlstring)(struct lua_State *L, const char *s, size_t l);
typedef const char* (*pfn_luaL_checkstring)(struct lua_State *L, int numArg);
typedef double (*pfn_luaL_checknumber)(struct lua_State *L, int numArg);

extern pfn_lua_pushnumber p_lua_pushnumber;
extern pfn_lua_pushstring p_lua_pushstring;
extern pfn_lua_pushcclosure p_lua_pushcclosure;
extern pfn_lua_setfield p_lua_setfield;
extern pfn_lua_newtable p_lua_newtable;
extern pfn_lua_tonumber p_lua_tonumber;
extern pfn_lua_tostring p_lua_tostring;
extern pfn_lua_toboolean p_lua_toboolean;
extern pfn_lua_gettop p_lua_gettop;
extern pfn_lua_pushboolean p_lua_pushboolean;
extern pfn_lua_pushinteger p_lua_pushinteger;
extern pfn_lua_pushlightuserdata p_lua_pushlightuserdata;
extern pfn_lua_setglobal p_lua_setglobal;
extern pfn_lua_pushnil p_lua_pushnil;
extern pfn_lua_pushlstring p_lua_pushlstring;
extern pfn_luaL_checkstring p_luaL_checkstring;
extern pfn_luaL_checknumber p_luaL_checknumber;

#define lua_pushnumber p_lua_pushnumber
#define lua_pushstring p_lua_pushstring
#define lua_pushcclosure p_lua_pushcclosure
#define lua_setfield p_lua_setfield
#define lua_newtable p_lua_newtable
#define lua_tonumber p_lua_tonumber
#define lua_tostring p_lua_tostring
#define lua_toboolean p_lua_toboolean
#define lua_gettop p_lua_gettop
#define lua_pushboolean p_lua_pushboolean
#define lua_pushinteger p_lua_pushinteger
#define lua_pushlightuserdata p_lua_pushlightuserdata
#define lua_setglobal p_lua_setglobal
#define lua_pushnil p_lua_pushnil
#define lua_pushlstring p_lua_pushlstring
#define luaL_checkstring p_luaL_checkstring
#define luaL_checknumber p_luaL_checknumber