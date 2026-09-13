# LuaJIT 2.1 C API Dynamic Headers (Anomaly Modded EXEs)

## Provenance & Purpose
These lightweight header files (`lua.h`, `lauxlib.h`, `lualib.h`) provide C API definitions and dynamic function pointer prototypes for **LuaJIT 2.1.0-beta3**, as bundled in *S.T.A.L.K.E.R. Anomaly 1.5.3 Modded EXEs* (`bin/LuaJIT.dll`).

## Architecture & Integration
Rather than statically linking against an import library (`lua51.lib` or `luajit.lib`) which would couple the client DLL to a specific compiler/linker toolchain and ABI version, `ZoneClient` uses dynamic runtime symbol resolution via `GetProcAddress` on Anomaly's loaded `LuaJIT.dll`.

- **Function Pointers**: Defined as `pfn_lua_*` types and declared as `p_lua_*` pointers in `lua.h`.
- **Macro Aliases**: Map standard Lua C API functions (e.g. `lua_pushnumber`, `lua_newtable`, `luaL_checkstring`) to the dynamically resolved function pointers.
- **Hooking**: Detoured in `src/hook/lua_hook.cpp` via MinHook on `luaL_openlibs`.

## License
LuaJIT is copyright (C) 2005-2023 Mike Pall.
Released under the MIT License. See [LICENSE.txt](LICENSE.txt).
