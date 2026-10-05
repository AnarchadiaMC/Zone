# Zone — Installation & User Guide

> A dedicated multiplayer survival server and injectable client DLL for S.T.A.L.K.E.R. Anomaly 1.5.3.

---

## Architecture Overview

Zone uses an **autonomous client architecture** with two install paths:

- **Proxy DLL (recommended):** `version.dll` is a drop-in proxy for `<Anomaly>\bin\`. It forwards all 15 `VERSION.dll` exports to the system DLL and asynchronously loads `ZoneClient.dll` from its own directory on startup — no `dxgi.dll` rename, no conflicts with AnomalyFSR or ReShade.
- **Injector (alternative):** `ZoneClient_Injector.exe` launches the game (`--launch`) or attaches to a running process (`--wait`, `--pid`) and stages `ZoneClient.dll` next to the game executable.
- **Zero-touch asset provisioning:** The release `gamedata\` tree is copied to the game root (merge) for the proxy path; under `--launch` the injector additionally pre-provisions all 19 Zone gamedata files (3 configs, 3 UI layouts, 2 localization files, 11 scripts) before the engine starts. On attach, the DLL's embedded `AssetProvisioner` also overwrites scripts, UI layouts, localization and the Zone online DLTX config (`mod_system_zone_online.ltx`) atomically, and writes `mod_system_zone_faction_relations.ltx` / `system.ltx_patch.ltx` only if missing so local edits survive.
- **Native X-Ray UI:** A native "Zone" button appears on Anomaly's home screen. It opens a native `CUIScriptWnd` server browser with direct connect and favorites.
- **xrRazom coexistence:** Both menu buttons are preserved. Zone refuses to connect while an xrRazom co-op session is active and goes dormant for as long as co-op owns the world.

The server and client speak a packed binary protocol over a single UDP port (**27015** by default). Server queries are answered on the same port; there is no separate query port.

---

## Part 1 — Dedicated Server Setup

### Windows

Release packages ship a prebuilt `server\zone-server.exe` — no build step is needed:

```bat
cd server
zone-server.exe
```

The default configuration is `zone_server.yaml` in the working directory. Point at another file or override individual values on the command line:

```bat
zone-server.exe --config path\to\zone_server.yaml --port 27015 --db-path zone_world.db --tick-rate 30
```

### Server CLI flags

| Flag | Default | Meaning |
|---|---|---|
| `--config <path>` | `zone_server.yaml` | YAML configuration file. Loaded and validated first. |
| `--port <int>` | `0` (use config) | Override the UDP listen port. |
| `--db-path <path>` | empty (use config) | Override the SQLite database path. |
| `--tick-rate <hz>` | `0` (use config) | Override the tick rate. |
| `--help` | — | Standard Go `flag` help. |

Overrides are re-validated after merging, and the same bounds as the YAML path apply (port 1–65535, tick rate 1–240, `max_players` > 0).

### Linux (headless)

```bash
cd zone-server
GOOS=linux GOARCH=amd64 go build -o zone-server ./cmd/server
./zone-server
```

Building from source on Windows:

```bat
cd zone-server
go build -o zone-server.exe ./cmd/server
zone-server.exe
```

On first start the server creates the embedded SQLite database (`zone_world.db`) in WAL mode with ten tables (`accounts`, `characters`, `character_inventory`, `world_items`, `schema_version`, `world_stashes`, `safe_zones`, `audit_log`, `faction_relations`, `ai_squads`), seeds the 12 canonical safe zones, the faction relation matrix and (when `ai_enabled`) four Cordon AI patrol squads. Operator edits in those tables survive restarts.

### Configuration reference (`zone_server.yaml`)

All 22 keys understood by `internal/config`. The shipped `zone_server.yaml` sets 19 of them; the rest fall back to the defaults below when omitted.

| Key | Default | In shipped YAML | Purpose |
|---|---|:---:|---|
| `port` | required (shipped: `27015`) | yes | UDP listen port (1–65535). Server and queries share it. |
| `tick_rate_hz` | required (shipped: `30`) | yes | Game loop rate (1–240 Hz). |
| `max_players` | required (shipped: `64`) | yes | Maximum concurrent sessions; further handshakes get Status 1 (full). |
| `db_path` | `"zone_world.db"` | yes | SQLite database file. |
| `log_level` | `"info"` | yes | `debug`, `info`, `warn` or `error`. |
| `emission_interval_min` | `120` if omitted (shipped file: `45`) | yes | Minutes between emission cycles. |
| `admin_pipe` | `\\.\pipe\zone_admin` (`/tmp/zone_admin.sock` on Unix) | yes | Admin channel path. Commands: `status`, `kick`, `ban`, `broadcast`. |
| `server_name` | `"Zone Online"` | yes | Name advertised in the server browser. |
| `map_name` | `"l01_escape"` | yes | Hosted level (only `l01_escape` is supported). |
| `mode` | `0` | yes | Free-form mode byte advertised in `SERVER_QUERY_RES`. |
| `locked` | `false` | yes | `true` rejects new connections (handshake Status 1). |
| `group_max_players` | `4` | yes | Max group size, clamped to 1–8 (the 0x007A wire capacity). |
| `invite_ttl_sec` | `60` | yes | Group invite lifetime; swept every tick. |
| `damage_budget_per_s` | `400` | yes | Rolling 1 s sustained damage clamp per attacker (hit registration). |
| `item_rate_per_s` | `5` | yes | Item action rate limit per session (shared by 0x007D and 0x007F). |
| `ai_enabled` | `true` | yes | Master switch for AI seeding, simulation, replication and AI packets. |
| `ai_online_radius_m` | `220` | yes | Legacy alias. When set to a non-220 value it pins both hysteresis radii (no hysteresis); otherwise the enter/leave defaults apply. |
| `ai_enter_radius_m` | `180` | no | A puppet enters a client's replication stream inside this 2D radius. |
| `ai_leave_radius_m` | `220` | no | A visible puppet leaves only past this radius (hysteresis). Clamped ≥ enter radius. |
| `ai_max_entities` | `64` | no | Maximum registered puppet squads; registrations beyond the cap are logged and skipped, and per-session in-range sets are capped nearest-first. |
| `world_item_ttl_min` | `60` (pointer; explicit `0` disables) | yes | Minutes before dropped world items expire via the 30 s sweep. |
| `world_item_max_per_level` | `500` | yes | Max persisted world item rows per level; drops beyond the cap are rejected and the client is resynced. |

---

## Part 2 — Client Building

Build the client with CMake and Visual Studio 2022:

```bat
cd zone-client
cmake -S . -B build -G "Visual Studio 17 2022" -A x64
cmake --build build --config Release
```

Output binaries in `zone-client\build\Release\`:

- `ZoneClient.dll` — the client runtime (22 `ZN_*` exports).
- `ZoneClient_Injector.exe` — the launcher and injector.
- `version.dll` — the drop-in `VERSION.dll` proxy.

All three use the static CRT (`/MT`); no Visual C++ redistributable is required.

---

## Part 3 — Drag-and-drop install (recommended)

1. Copy **both** `version.dll` and `ZoneClient.dll` from the package `client\` folder into `<Anomaly>\bin\` (the folder that contains `AnomalyDX11.exe`). The proxy loads `ZoneClient.dll` from the same directory; the DLL must be beside the proxy, not in the game root.
2. Copy the package `gamedata\` folder into `<Anomaly>\` and merge when prompted. This places all 19 Zone files (configs, UI layouts, localization, scripts) under `<Anomaly>\gamedata\`.
3. Launch the game normally (or through MO2 / your mod organizer). On startup `version.dll` loads `ZoneClient.dll`; the DLL provisions anything missing and stages identity in `<Anomaly>\appdata\zone_identity.ltx` (the engine's `$app_data_root$`).
4. In the main menu, both buttons appear: the native **Zone** button, and the xrRazom co-op button if xrRazom is installed.

Do not mix install paths: if `version.dll` is installed, you do not need the injector, and vice versa.

### xrRazom coexistence

Zone refuses to connect while an xrRazom co-op session is active (`Busy - xrRazom co-op is active`). Disconnect from co-op first — and do not host or join co-op while a Zone session is connected. Running both at once would desync alife and double-spawn proxies. While co-op owns the world, Zone's networking, input hooks, damage handling and AI spawning all stay dormant; shared binds such as `kXRR_CHAT` are never consumed by Zone.

---

## Part 4 — Injector install (alternative)

Use this instead of the proxy to control exactly when the DLL loads, or to use `--launch` provisioning on a fresh install. Keep `ZoneClient_Injector.exe`, `ZoneClient.dll` and the shipped `gamedata` tree together (the standard `dist` layout). The injector copies `ZoneClient.dll` next to the game executable before injecting, and in `--launch` mode it also pre-provisions all 19 Zone gamedata files, so the first run is one command. Always inject before opening the Zone browser in-game.

| Mode / option | Meaning |
|---|---|
| `--launch <exe> [args...]` | Spawn the game suspended, resume it, wait for Lua readiness, then inject. Pre-provisions gamedata before engine start. |
| `--wait` | (Default) Poll for a running Anomaly process and inject. |
| `--pid <pid>` | Inject directly into the given process ID. |
| `--dll <path>` | Use a custom `ZoneClient.dll` path (default: next to the injector). |
| `--ephemeral` | With `--launch`: wait for the game to exit, then purge the staged Zone files. |

Examples:

```bat
:: Launch and inject in one command
ZoneClient_Injector.exe --launch "C:\Anomaly\bin\AnomalyDX11.exe" -dbg -smap4096

:: Background wait mode, then launch the game through MO2
ZoneClient_Injector.exe --wait

:: Inject into an already-running game
ZoneClient_Injector.exe --pid 14280
```

**First-run restart note.** Under `--launch` the engine starts after provisioning, so the files are visible immediately. With `--wait` or `--pid` the game is already running and Anomaly caches its filesystem mounts at init: the DLL is still staged and injected, but if this is the first time Zone gamedata is provisioned, restart the game (or use `--launch` next time) before the new files become visible to the engine.

---

## Part 5 — Verifying the connection

1. Start the server. The log prints `Starting Zone Server` with the port, and a `Handshake` line with the client address for every successful connection attempt.
2. In the game, click the native **Zone** button on the main menu.
3. In the browser, enter the address (default `127.0.0.1`), the port `27015` and your callsign, then click **Connect**. If the server is reachable, its row/query reply shows in the browser (name, map `l01_escape`, player counts, mode, lock state, protocol and tick rate).
4. The footer stays open while the handshake is pending and polls `ZoneNet:IsConnected()`: `Connected to <ip>:<port>` on success, or `Failed - server unreachable, see xray log` after roughly 16 seconds (the DLL gives up after 15 s / 5 tries). A version mismatch, full server or ban is surfaced through `ZN_GetLastError`.
5. In game, the HUD shows `[ZO] Online` with the current ping. Remote players appear as proxies; AI patrol squads appear near the Cordon.

If the browser reports `DLL missing - inject ZoneClient first`, the client DLL was not loaded: with the proxy path check that both `version.dll` and `ZoneClient.dll` are in `bin\`; with the injector path inject before opening the zone menu.

For an automated end-to-end check without the game client, run the committed UDP harness against a live server:

```bat
python tools/loadtest/zone_loadtest.py 27015 --clients 12 --duration 12
```

It performs 12 real binary-protocol handshakes and exercises snapshots, groups, item drop/pickup/duplicate-pickup rejection, AI AoI transitions and 300 malformed packets, then prints `LOAD TEST PASSED`. See [tools/loadtest/README.md](tools/loadtest/README.md) for coverage and prerequisites.

---

## Part 6 — In-game usage

### Server browser

- **Direct Connect:** default `127.0.0.1:27015`.
- **Favorites / history:** persisted in `appdata\zone_identity.ltx` under the game root, atomic writes with a direct-write fallback.
- **Async scan:** results stream in as servers answer; unanswered endpoints dim and back off. Sortable headers, name/map/ping filters, hide-full/empty/locked switches and a details pane (protocol, tick rate) are available. Double-click a server to connect.

### Seamless join

A new account receives `SHOW_START` (0x0071) and the native faction/loadout dialog opens. Pick a faction and loadout and click **Start**: the server validates the choice, creates the character plus starter inventory in SQLite, then the client starts `l01_escape`, applies the server spawn/faction and materializes the synced inventory. Returning accounts go straight to their saved spawn. Only `l01_escape` is hosted; any level change requires a reconnect.

### Factions, groups and chat

- Faction relations are server-authoritative and ship to clients as `configs/mod_system_zone_faction_relations.ltx`; hostile factions are flagged as enemies, and the server rejects same-faction and same-group damage. Movement is client-authoritative by owner decision: the server accepts finite, in-bounds transforms as-is with no speed/teleport validation or corrections. Player-vs-player hits are validated: the client reports a proxy hit with `OpDamageNotify` (0x0050), the server checks attacker session, safe zones on either side, friendly fire, 3D range (≤ 300 m), damage sanity, a 150 single-hit clamp and the rolling damage budget, then relays the accepted hit to the victim, whose client applies it via `change_health`. The path is implemented but not yet exercised by two live clients, and damage is applied 1:1 with no server-side armor scaling.
- Mixed-faction groups are supported. Chat commands: `/invite <callsign>`, `/accept`, `/decline`, `/leave`, `/group`.
- Open chat with the game's `kXRR_CHAT` binding when the executable exposes it, otherwise `Y`.

### Safe zones

Twelve canonical zones (Rookie Village, 100 Rads Bar, Yantar Bunker, Flea Market, etc.) protect every faction equally: weapons are holstered, fire is blocked, damage is suppressed in both directions and client-side AI hostility is suppressed, with a PDA notification and HUD banner on entry/leave. Safe-zone protection does not depend on faction, group or war state.

### Item ledger and world items

Dropping, picking up and consuming items are server-authoritative (0x007D/0x007E): the server owns the inventory rows and world item ids, rejects duplicate pickups and rate-limits actions. Dropped items persist in the world, sync to players entering the area and expire after the configured TTL. Container deposit/withdraw (0x007F/0x0080) is implemented and tested server-side, but no client container UI drives it yet.

### AI patrols

Server-authoritative patrol squads replicate to clients inside 180 m (leaving past 220 m) and stream state at ~30 Hz. Puppets rotate and animate but do not fight in this wave.

---

## Troubleshooting

- **`zone_identity.ltx.tmp` orphan:** a harmless leftover from an interrupted atomic write. Both writers (C++ identity and Lua favorites) clean stale `.tmp` files and preserve each other's sections; deleting one by hand is safe.
- **Stale identity / favorites:** delete `<Anomaly>\appdata\zone_identity.ltx` only as a last resort; it also resets favorites and history.
- **Server does not appear in LAN scan:** Windows Firewall may be blocking UDP `27015`; allow the server executable or connect by IP directly.
- **Port conflict:** change `port` in `zone_server.yaml` or pass `--port`, and use the same port in the browser.
- **AI absent:** check `ai_enabled: true` and that the `ai_squads` table has rows (it is seeded when empty).
- **Items not dropping/picking up:** item actions are gated on being connected and outside an xrRazom session; watch for the resync notice if the server rejects an action.
