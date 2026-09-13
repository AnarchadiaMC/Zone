# Zone Online — Installation & User Guide

> A dedicated multiplayer survival server and injectable client DLL for S.T.A.L.K.E.R. Anomaly 1.5.3.

---

## Architecture Overview

Zone Online uses an **autonomous client injection architecture**:
- **No Proxy DLLs**: No `dxgi.dll` or proxy loading required. No conflicts with AnomalyFSR or ReShade.
- **Zero-Touch Asset Provisioning**: When `ZoneClient.dll` is injected, it automatically provisions necessary configuration (`mod_system_zone_online.ltx` via DLTX) and UI layouts (`zone_ui_server_list.xml`) if they are missing.
- **Native X-Ray UI**: A native "Zone Online" button appears directly on Anomaly's home screen. Clicking it opens a native S.T.A.L.K.E.R. Server Browser with direct IP connect and favorite servers list.
- **Automated Launcher/Injector**: `ZoneClient_Injector.exe` can launch the modified EXEs and inject simultaneously, or run as a standalone injector.

---

## Part 1 — Dedicated Server Setup

### Windows
```bat
cd F:\AnomalyDev\zone-online\zone-server
go build -o zone-server.exe ./cmd/server
zone-server.exe
```

### Linux (Headless)
```bash
cd zone-online/zone-server
GOOS=linux GOARCH=amd64 go build -o zone-server ./cmd/server
./zone-server
```

### Server Configuration (`zone_server.yaml`)
```yaml
port: 27015          # UDP port to listen on
tick_rate_hz: 30     # Game simulation tick rate
max_players: 64      # Max concurrent stalkers
db_path: zone_world.db
log_level: info
emission_interval_min: 45
```

The server automatically initializes the embedded SQLite database (`zone_world.db`) with 7 tables and seeds all 8 canonical Zone safe zones (Rookie Village, 100 Rads Bar, Yantar Bunker, Flea Market, etc.).

---

## Part 2 — Client Building

Build the client using CMake and Visual Studio 2022:

```bat
cd F:\AnomalyDev\zone-online\zone-client
cmake -S . -B build -G "Visual Studio 17 2022" -A x64
cmake --build build --config Release
```

Output binaries in `zone-client\build\Release\`:
- `ZoneClient.dll` — The client DLL.
- `ZoneClient_Injector.exe` — The automated launcher & injector.

---

## Part 3 — Automated Launch & Injection

### Option A: Launch & Inject in One Command (Recommended)
You can use `ZoneClient_Injector.exe` to launch your modified Anomaly executable and inject `ZoneClient.dll` automatically:

```bat
ZoneClient_Injector.exe --launch "D:\Anomaly\bin\AnomalyDX11.exe"
```
Or with custom launch arguments:
```bat
ZoneClient_Injector.exe --launch "D:\Anomaly\bin\AnomalyDX11.exe" -smap4096 -dbg
```

### Option B: Background Wait Mode
Run the injector in wait mode, then launch Anomaly through your mod organizer (MO2) or launcher:

```bat
ZoneClient_Injector.exe --wait
```
The injector detects `AnomalyDX11.exe`, `AnomalyDX11AVX.exe`, `VerifiedDX11.exe`, or `xrEngine.exe` when it starts and injects automatically.

---

## Part 4 — In-Game Usage

### Home Screen
1. When Anomaly opens, look at the main menu.
2. Below the standard menu options, a native **Zone Online** button appears.
3. Click **Zone Online** to open the Server Browser.

### Server Browser Features
- **Direct Connect**: Enter the server IP address (default `127.0.0.1`), Port (`27015`), and your Callsign/Nickname, then click **Connect**.
- **Favorite Servers**: Save your favorite servers for one-click connection. Favorites are persisted in `%APPDATA%\zone_identity.ltx`.
- **Double-Click Connect**: Double-clicking any server in your favorites list connects immediately.

### In-Game HUD & Safe Zones
- A subtle HUD indicator in the corner displays your real-time connection status and ping.
- When entering a designated safe zone (e.g. Rookie Village or Rostok Bar), weapons are automatically holstered, damage is suppressed, and a PDA notification confirms peaceful territory.