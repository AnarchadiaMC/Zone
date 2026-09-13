# ZoneClient — S.T.A.L.K.E.R. Anomaly Multiplayer Client & Injector

`ZoneClient` is the client-side runtime layer for **Zone Online**, an authoritative multiplayer survival architecture for *S.T.A.L.K.E.R. Anomaly 1.5.3*.

It consists of two native C++ components:
1. **`ZoneClient.dll`**: A lightweight runtime DLL that hooks into Anomaly's engine via MinHook, initializes persistent HWID/UUID identity, provisions missing game assets, handles non-blocking binary UDP telemetry, and exposes the `ZoneNet` Lua API directly into Anomaly's LuaJIT environment.
2. **`ZoneClient_Injector.exe`**: An automated launcher and injector capable of launching modified game executables and injecting `ZoneClient.dll` alongside process initialization, or running in background wait mode.

---

## Key Architectural Features

### 1. Zero-Touch Autonomous Asset Provisioning (`AssetProvisioner`)
Unlike traditional mods that require manual file extraction, directory creation, or editing `system.ltx`:
- When `ZoneClient.dll` attaches to the game process (`DLL_PROCESS_ATTACH`), it detects the root game directory.
- It automatically creates required directories and writes out missing assets from embedded string constants:
  - **DLTX Patch**: `gamedata/configs/mod_system_zone_online.ltx` (auto-merged into `system.ltx` by Anomaly Modded EXEs).
  - **UI XML Layout**: `gamedata/configs/ui/zone_ui_server_list.xml` (layout definition for the native server browser).
- **Zero manual file copying or configuration editing is needed.**

### 2. Native X-Ray UI Integration (No ImGui / No DirectX Hooking)
- Operates entirely without external overlay layers (like Dear ImGui) or DirectX Present hooks.
- Interfaces directly with Anomaly's native UI system:
  - Injects a native 3-state button (`CUI3tButton`) labeled **"Zone Online"** into Anomaly's home screen stack below vanilla buttons (`zone_menu_patch.script`).
  - Opens a native `CUIScriptWnd` server browser dialog (`zone_ui_server_list.script` + XML) with Direct Connect (IP, Port, Callsign) and persistent Favorite Servers.
  - Renders HUD network status and safe zone alerts natively via X-Ray PDA news tips and HUD statics (`zone_hud.script`).

### 3. Dynamic LuaJIT Runtime Hooking
- Uses **MinHook** to detour `luaL_openlibs` in `LuaJIT.dll`.
- Uses dynamic runtime symbol resolution via `GetProcAddress` on `LuaJIT.dll` for all Lua C API functions (`lua_push*`, `luaL_check*`, `lua_newtable`, etc.), eliminating external `.lib` file dependencies.
- Injects the global `ZoneNet` table into `_G` exposing 8 C functions:
  - `ZoneNet:Connect(ip, port, uuid, hwid, nick)`
  - `ZoneNet:Disconnect()`
  - `ZoneNet:SendTransform(x, y, z, yaw, pitch, vx, vy, vz, animflags)`
  - `ZoneNet:PollEvent()`
  - `ZoneNet:IsInSafeZone()`
  - `ZoneNet:IsConnected()`
  - `ZoneNet:GetSessionInfo()`
  - `ZoneNet:SendChatText(text)`

### 4. Background WinSock2 UDP Networking
- Dedicated networking thread operating a non-blocking UDP socket.
- Handles packet fragmentation, 12-byte binary protocol headers (`0x5A4F` magic), monotonic sequence counters, and reliable ACK queues with 500ms retransmit timers.
- Transmits local actor transform telemetry at a fixed 30 Hz ($33.3\text{ ms}$).
- Thread-safe 256-entry ring buffer for events polled non-blockingly by Anomaly's Lua tick loop.

---

## Directory Structure

```
zone-client/
├── 3rdparty/
│   ├── lua/                # LuaJIT header definitions & dynamic function pointers
│   └── minhook/            # MinHook source & headers (buffer, hook, trampoline, hde64)
├── injector/
│   └── injector_main.cpp   # Automated launcher and Win32 remote thread injector
├── src/
│   ├── hook/
│   │   ├── lua_hook.h      # Detour interface for luaL_openlibs
│   │   └── lua_hook.cpp
│   ├── identity/
│   │   ├── identity.h      # HWID (FNV-1a) & UUID v4 identity engine
│   │   └── identity.cpp
│   ├── lua/
│   │   ├── zone_bindings.h # ZoneNet Lua C functions declarations
│   │   └── zone_bindings.cpp
│   ├── net/
│   │   ├── udp_client.h    # WinSock2 background networking thread
│   │   └── udp_client.cpp
│   ├── protocol/
│   │   └── packets.h       # Binary packet definitions (#pragma pack(push, 1))
│   ├── provision/
│   │   ├── asset_provisioner.h   # Autonomous asset provisioner
│   │   └── asset_provisioner.cpp
│   └── main.cpp            # DllMain lifecycle & initialization thread
├── CMakeLists.txt          # Multi-target CMake build configuration
└── README.md
```

---

## Building

### Requirements
- **Operating System**: Windows 10 / 11 (x64)
- **Compiler**: Visual Studio 2022 (MSVC v143 toolset) with C++17 support
- **Build System**: CMake 3.20+
- **Windows SDK**: 10.0.19041.0 or newer

### Build Commands
```bat
# From repository root or zone-client directory:
cd zone-client
cmake -S . -B build -G "Visual Studio 17 2022" -A x64
cmake --build build --config Release
```

### Build Outputs
- `build\Release\ZoneClient.dll` — Standalone injectable client DLL.
- `build\Release\ZoneClient_Injector.exe` — Standalone launcher / injector executable.

---

## Injector Usage

`ZoneClient_Injector.exe` supports multiple operation modes:

```
Usage: ZoneClient_Injector.exe [options]

Modes:
  --launch <exe> [args...]   Spawn game process (CREATE_SUSPENDED -> Resume) and inject
  --wait                     (Default) Poll for running Anomaly processes and inject
  --pid <pid>                Inject directly into specified process ID

Options:
  --dll <path>               Custom path to ZoneClient.dll (default: next to injector)
  --help, -h                 Display help information
```

### Examples

#### Launch & Inject Automatically:
```bat
ZoneClient_Injector.exe --launch "D:\Anomaly\bin\AnomalyDX11.exe" -dbg -smap4096
```

#### Run in Background Wait Mode:
```bat
ZoneClient_Injector.exe --wait
```

#### Inject into a Running Game PID:
```bat
ZoneClient_Injector.exe --pid 14280
```