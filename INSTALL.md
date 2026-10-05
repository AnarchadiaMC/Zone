# Zone — Installation & User Guide

> A dedicated multiplayer survival server and injectable client DLL for S.T.A.L.K.E.R. Anomaly 1.5.3.

---

## Architecture Overview

Zone uses an **autonomous client architecture** with two install paths:
- **Proxy DLL (recommended)**: `version.dll` is a drop-in proxy for `<Anomaly>\bin\`. It forwards all 15 `VERSION.dll` exports to the system DLL and loads `ZoneClient.dll` from its own directory on startup — no `dxgi.dll` rename, no conflicts with AnomalyFSR or ReShade.
- **Injector (alternative)**: `ZoneClient_Injector.exe` launches the game (`--launch`) or attaches to a running process (`--wait`, `--pid`) and stages `ZoneClient.dll` next to the game executable.
- **Zero-Touch Asset Provisioning**: The release `gamedata\` tree is copied to the game root (merge) for the proxy path; in `--launch` mode the injector additionally pre-provisions all 19 Zone gamedata files (configs, UI layouts, scripts, localization) before the engine starts. On attach, the DLL's embedded `AssetProvisioner` also overwrites scripts, UI layouts, localization and the Zone online DLTX config (`mod_system_zone_online.ltx`) atomically to match the DLL, and writes `mod_system_zone_faction_relations.ltx` / `system.ltx_patch.ltx` only if missing so local edits survive.
- **Native X-Ray UI**: A native "Zone" button appears directly on Anomaly's home screen. Clicking it opens a native S.T.A.L.K.E.R. Server Browser with direct IP connect and favorite servers list.
- **Automated Launcher/Injector**: `ZoneClient_Injector.exe` can launch the modified EXEs and inject simultaneously, or run as a standalone injector.

---

## Part 1 — Dedicated Server Setup

### Windows
Release packages ship a prebuilt `server\zone-server.exe` — no build step is needed:

```bat
cd server
zone-server.exe
```

Use `zone-server.exe --config path\to\zone_server.yaml` to point at another config file (also `--port`, `--db-path`, `--tick-rate` overrides). The defaults come from `zone_server.yaml` in the working directory (all 13 keys listed below). The game protocol and server queries share the single UDP port **27015**; there is no separate query port in 0.4.0 (`27016` is not used).

Building from source instead:

```bat
cd zone-server
go build -o zone-server.exe ./cmd/server
zone-server.exe
```

### Linux (Headless)
```bash
cd zone-server
GOOS=linux GOARCH=amd64 go build -o zone-server ./cmd/server
./zone-server
```

### Server Configuration (`zone_server.yaml`)
```yaml
port: 27015                          # UDP port to listen on
tick_rate_hz: 30                     # Game simulation tick rate
max_players: 64                      # Max concurrent stalkers
db_path: "zone_world.db"
log_level: "info"
emission_interval_min: 45
admin_pipe: "\\\\.\\pipe\\zone_admin"
server_name: "Zone Online"           # Name advertised in the server browser
map_name: "l01_escape"               # Hosted level (only l01_escape for now)
mode: 0                              # Server mode
locked: false                        # true rejects new connections
group_max_players: 4                 # Max players per group
invite_ttl_sec: 60                   # Group invite lifetime (seconds)
```

The server automatically initializes the embedded SQLite database (`zone_world.db`) with 8 tables and seeds all 12 canonical Zone safe zones (Rookie Village, 100 Rads Bar, Yantar Bunker, Flea Market, etc.).

---

## Part 2 — Client Building

Build the client using CMake and Visual Studio 2022:

```bat
cd zone-client
cmake -S . -B build -G "Visual Studio 17 2022" -A x64
cmake --build build --config Release
```

Output binaries in `zone-client\build\Release\`:
- `ZoneClient.dll` — The client DLL.
- `ZoneClient_Injector.exe` — The automated launcher & injector.
- `version.dll` — The drop-in VERSION.dll proxy (loads `ZoneClient.dll` from `bin\`).

---

## Part 3 — Drag-and-drop install (recommended)

1. Copy `version.dll` and `ZoneClient.dll` from the package `client\` folder into `<Anomaly>\bin\` (the folder that contains `AnomalyDX11.exe`).
2. Copy the package `gamedata\` folder into `<Anomaly>\` and merge when prompted. This places all 19 Zone files (configs, UI layouts, localization, scripts) under `<Anomaly>\gamedata\`.
3. Launch the game normally (or through MO2 / your mod organizer). On startup `version.dll` loads `ZoneClient.dll` from `bin\`; the DLL provisions anything missing and stages identity.
4. In the main menu, both buttons appear: the native **Zone** button, and the xrRazom co-op button if xrRazom is installed.
5. Zone refuses to connect while an xrRazom co-op session is active (`Busy - xrRazom co-op is active`). Disconnect from co-op first — and do not host/join co-op while a Zone session is connected.

No Visual C++ redistributable is required: all release binaries use the static CRT (`/MT`). The proxy path needs no launcher and survives game updates as long as `version.dll` and `ZoneClient.dll` stay in `bin\`.

---

## Part 4 — Injector install (alternative)

Use this instead of the proxy if you prefer explicit injection (for example, to control exactly when the DLL loads, or to use `--launch` provisioning on a fresh install). Keep `ZoneClient_Injector.exe`, `ZoneClient.dll` and the shipped `gamedata` tree together (the standard `dist` layout). The injector copies `ZoneClient.dll` next to the game executable automatically before injecting, and in `--launch` mode it also pre-provisions all 19 Zone gamedata files into the game root, so the first run is one command. Always inject BEFORE opening the Zone browser in-game.

### Option A: Launch & Inject in One Command
You can use `ZoneClient_Injector.exe` to launch your modified Anomaly executable and inject `ZoneClient.dll` automatically:

```bat
ZoneClient_Injector.exe --launch "<path-to-anomaly>\bin\AnomalyDX11.exe"
```
Or with custom launch arguments:
```bat
ZoneClient_Injector.exe --launch "<path-to-anomaly>\bin\AnomalyDX11.exe" -smap4096 -dbg
```

### Option B: Background Wait Mode
Run the injector in wait mode, then launch Anomaly through your mod organizer (MO2) or launcher:

```bat
ZoneClient_Injector.exe --wait
```
The injector detects `AnomalyDX11.exe`, `AnomalyDX11AVX.exe`, `VerifiedDX11.exe`, or `xrEngine.exe` when it starts and injects automatically.

Note: with `--wait` or `--pid` the game is already running, and Anomaly's filesystem caches its mounts at init. The DLL is still staged next to the game exe, but if this is the first time Zone files are provisioned, restart the game (or use `--launch` next time) before the new `gamedata` files become visible to the engine.

---

## Part 5 — In-Game Usage

### Home Screen
1. When Anomaly opens, look at the main menu.
2. Below the standard menu options, a native **Zone** button appears.
3. Click **Zone** to open the Server Browser.

### Server Browser Features
- **Direct Connect**: Enter the server IP address (default `127.0.0.1`), Port (`27015`), and your Callsign/Nickname, then click **Connect**.
- **Favorite Servers**: Save your favorite servers for one-click connection. Favorites are persisted in `appdata\zone_identity.ltx` under the game root (the engine's `$app_data_root$`).
- **Double-Click Connect**: Double-clicking any server in your favorites list connects immediately.
- **Status footer flow (dialog stays open)**: After **Connect**, the browser does NOT auto-close. The footer shows `Status: Connecting to <ip>:<port>...`, then polls `ZoneNet:IsConnected()` (~2 Hz in `zone_ui_server_list.script:Update()`): `Connected to <ip>:<port>` on handshake success, or `Failed - server unreachable, see xray log` after ~16s (matches the DLL 15s/5-try budget in `udp_client.cpp`). The browser stays open only while the handshake is pending.
- **Seamless join (no manual singleplayer load)**: Once connected, a new account receives `SHOW_START` (`0x0071`) and the native faction/loadout dialog opens automatically; a returning account goes straight to its saved spawn. Pick a faction and loadout and click **Start**: the server creates the character and starter inventory in SQLite, then the client starts `l01_escape`, applies the server faction/spawn, and materializes the synced inventory. Only `l01_escape` is hosted for now; arbitrary level loading is tracked in ROADMAP.md.
- **DLL must be loaded first**: The browser gates Connect on `zone_net.is_client_loaded()` (`ffi.load("ZoneClient")` in `zone_net.script`). With the proxy install the DLL is loaded at game startup; with the injector, inject first (Option A `--launch` or Option B `--wait`) before opening the Zone menu. If `ZoneClient.dll` is not loaded, Connect aborts with `DLL missing - inject ZoneClient first`.
- **Mutual exclusion with xrRazom co-op**: Zone refuses to connect while an xrRazom co-op session is active (`XrrNet():IsConnected()/IsListening()` or `XrrIsConnected()/XrrIsHosted()` in `zone_ui_server_list.script:OnConnect()` and `zone_net.script:on_tick()`), showing `Busy - xrRazom co-op is active`. Disconnect from co-op first; conversely, do not host/join xrRazom while a Zone session is connected. Running both proxy systems at once desyncs alife and double-spawns dummies.
- **Stale `zone_identity.ltx.tmp` cleanup**: if you see a `zone_identity.ltx.tmp` orphan next to `zone_identity.ltx` in the game's `appdata` folder, it is a harmless leftover from an interrupted write. Both writers are safe: the C++ identity writer merges the canonical `[zone_identity]` keys (`client_uuid`, `hwid_hash`, `nickname`) and writes atomically with Win32 temp + `MoveFileExW`, cleaning stale `.tmp` files at startup; the Lua favorites writer uses temp + rename with a direct-write fallback. Deleting a leftover `.tmp` file by hand is safe, and both writers preserve each other's sections (identity, `[zone_favorites]`, `[zone_history]`).

### Playing Together — Factions & Groups
- **Server-authoritative faction relations**: the server decides who is hostile to whom and ships that table to clients as `configs/mod_system_zone_faction_relations.ltx` (a DLTX `mod_system_` config the engine auto-merges). Players of hostile factions fight each other in the world according to that table.
- **Safe zones**: every faction is safe inside a designated safe zone; safe-zone suppression overrides faction hostility.
- **Mixed-faction groups** are supported. Use these in-game chat commands:
  - `/invite <callsign>` — invite a player to your group
  - `/accept` / `/decline` — answer a pending invitation
  - `/leave` — leave your current group
  - `/group` — list your current group members
- **Opening chat**: press the chat key. Zone uses the game's `kXRR_CHAT` binding when the executable exposes it, otherwise it falls back to `Y`.
- Only `l01_escape` is hosted for now; additional levels are tracked in ROADMAP.md.

### In-Game HUD & Safe Zones
- A subtle HUD indicator in the corner displays your real-time connection status and ping.
- When entering a designated safe zone (e.g. Rookie Village or Rostok Bar), weapons are automatically holstered, damage is suppressed, and a PDA notification confirms peaceful territory.