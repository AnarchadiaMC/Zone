# Zone Online — Build Walkthrough & Architecture Reference

## Overview of Latest Changes

Based on architectural requirements, Zone Online has transitioned to an **autonomous client architecture**:
1. **Zero ImGui / DirectX Hooking**: Removed Dear ImGui, D3D11 Present hooks, and related overhead.
2. **Zero Proxy Auto-Loading**: Removed `dxgi.dll` proxy forwarding and `.def` exports. No conflicts with ReShade or AnomalyFSR.
3. **Native X-Ray Main Menu Button**: Injected a native `CUI3tButton` directly onto Anomaly's home screen stack below the main menu options.
4. **Native X-Ray Server Browser Dialog**: Complete `CUIScriptWnd` dialog (`zone_ui_server_list.script` + `zone_ui_server_list.xml`) with:
   - Direct IP and Port connection input.
   - Callsign / Nickname input.
   - Favorite / Saved servers history with Add, Remove, and Double-Click-to-Connect.
   - Persistence in `%APPDATA%\zone_identity.ltx`.
5. **Zero-Touch Autonomous Provisioning (`AssetProvisioner`)**:
   - `ZoneClient.dll` embeds all required DLTX configs (`mod_system_zone_online.ltx`), XML layouts (`zone_ui_server_list.xml`), and identity definitions.
   - On injection, it automatically inspects the game root and provisions any missing files. Zero manual copying required.
6. **Automated Launcher & Injector (`ZoneClient_Injector.exe`)**:
   - `--launch <exe_path>`: Launches the modified Anomaly executable and injects `ZoneClient.dll` simultaneously.
   - `--wait`: Background wait mode that detects any running Anomaly executable (`AnomalyDX11.exe`, `AnomalyDX11AVX.exe`, etc.) and injects.
   - `--pid <pid>`: Immediate injection into a running PID.
   - `--dll <path>`: Custom DLL path specification.

---

## File Summary

### C++ Client (`zone-client/`)
| File | Status | Description |
|:---|:---|:---|
| `CMakeLists.txt` | Configured | Two clean targets: `ZoneClient` (SHARED) and `ZoneClient_Injector` (EXE). MinHook + WinSock2 + Lua headers. |
| `src/main.cpp` | Complete | Clean DllMain: initializes MinHook, Identity, runs `AssetProvisioner::EnsureAssets()`, and starts network thread. |
| `src/provision/asset_provisioner.h/.cpp` | Complete | Embedded DLTX and UI XML auto-provisioner. Writes missing files to game root on attach. |
| `src/net/udp_client.h/.cpp` | Complete | Non-blocking WinSock2 UDP client, state machine, 30Hz transform sync, ACK queue. |
| `src/hook/lua_hook.h/.cpp` | Complete | MinHook detour on `luaL_openlibs` in `LuaJIT.dll` to register `ZoneNet` table into `_G`. |
| `src/lua/zone_bindings.h/.cpp` | Complete | 8 Lua C functions exposed under `ZoneNet`: `Connect`, `Disconnect`, `SendTransform`, `PollEvent`, `IsInSafeZone`, `IsConnected`, `GetSessionInfo`, `SendChatText`. |
| `injector/injector_main.cpp` | Complete | Automated injector with `--launch`, `--wait`, `--pid`, and `--dll` modes. |

### Lua Gamedata (`gamedata/`)
| File | Status | Description |
|:---|:---|:---|
| `configs/mod_system_zone_online.ltx` | Complete | DLTX definition for `[zone_proxy_stalker]`, auto-merged into `system.ltx`. |
| `configs/ui/zone_ui_server_list.xml` | Complete | S.T.A.L.K.E.R.-themed XML layout for Server Browser dialog. |
| `scripts/zone_menu_patch.script` | Complete | Hooks `ui_main_menu.main_menu:InitControls` to inject native "Zone Online" button. |
| `scripts/zone_ui_server_list.script` | Complete | Native `CUIScriptWnd` server browser dialog with favorites and direct connect. |
| `scripts/zone_hud.script` | Complete | Native in-game HUD indicator (connection status, ping) and safe zone notifications. |
| `scripts/zone_main.script` | Complete | Bootstrap, identity loading, callback registration. |
| `scripts/zone_net.script` | Complete | 30Hz tick, LuaJIT FFI float unpacking, packet opcode dispatcher. |
| `scripts/zone_dummy.script` | Complete | Remote player dummy avatars with cubic Hermite spline interpolation. |
| `scripts/zone_safezone.script` | Complete | Safe zone enforcement, weapon lowering, damage suppression. |
| `scripts/zone_ai_proxy.script` | Complete | Server-authoritative AI puppet synchronization. |
| `scripts/zone_worldevent.script` | Complete | Emission and mutant raid synchronization. |

### Go Dedicated Server (`zone-server/`)
| Package | Tests | Description |
|:---|:---|:---|
| `cmd/server` | Verified | `zone-server.exe` compiles with 0 errors. |
| `internal/database` | Passed (0.64s) | WAL-mode SQLite, 7 tables, full CRUD, async write queue. |
| `internal/game` | Passed (0.59s) | 30Hz ticker, spatial grid, 8 safe zones, AoI 220m interest management. |
| `internal/protocol` | Passed (0.36s) | Binary protocol LE encoding/decoding, 16 opcodes, 12-byte header. |
| `internal/network` | Complete | Non-blocking UDP server, 8-worker goroutine pool, reliable ACK queue. |
| `internal/ai` | Complete | Two-tier AI simulation: 30Hz online / 1Hz offline. |

---

## Verification Results
- `go test ./...` in `zone-server`: **All tests passed** (`ok database`, `ok game`, `ok protocol`).
- `go build ./...` in `zone-server`: **Compiled successfully** (`zone-server.exe` generated).
- `cmake -S zone-client -B zone-client/build`: **Configured successfully** with exit code 0.