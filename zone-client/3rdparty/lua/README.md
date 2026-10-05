# LuaJIT C API Headers (Reference Only)

## Provenance
These header files (`lua.h`, `lauxlib.h`, `lualib.h`) provide C API declarations and dynamic function pointer prototypes for **LuaJIT 2.1.0-beta3**, as bundled in *S.T.A.L.K.E.R. Anomaly 1.5.3 Modded EXEs*.

## Status in ZoneClient
These headers are **not compiled into ZoneClient** and are retained for reference only.

Anomaly's xray-monolith engine links LuaJIT **statically** into the game executable; no `LuaJIT.dll` or `lua51.dll` module exists at runtime. The previous `luaL_openlibs` MinHook detour and the `GetProcAddress`-based Lua binding registration (`lua_bindings.cpp`) were therefore dead code and have been removed.

The supported integration path is **LuaJIT FFI**: `gamedata/scripts/zone_net.script` loads `ZoneClient.dll` via `ffi.load("ZoneClient")` and calls the `ZN_*` C exports declared in `src/lua/zone_bindings.cpp`.

## License
LuaJIT is copyright (C) 2005-2023 Mike Pall.
Released under the MIT License. See [LICENSE.txt](LICENSE.txt).
