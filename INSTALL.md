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

All 32 keys understood by `internal/config`. The shipped `zone_server.yaml` sets all 32 of them; omitted keys fall back to the defaults below.

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
| `map_name` | `"l01_escape"` | yes | Initial/menu spawn level. In-game travel can switch to any installed level (see below). |
| `mode` | `0` | yes | Free-form mode byte advertised in `SERVER_QUERY_RES`. |
| `locked` | `false` | yes | `true` rejects new connections (handshake Status 1). |
| `group_max_players` | `4` | yes | Max group size, clamped to 1–8 (the 0x007A wire capacity). |
| `invite_ttl_sec` | `60` | yes | Group invite lifetime; swept every tick. |
| `damage_budget_per_s` | `400` | yes | Rolling 1 s sustained damage clamp per attacker (hit registration). |
| `item_rate_per_s` | `5` | yes | Item action rate limit per session (shared by 0x007D, 0x007F, 0x0081 and 0x0083). |
| `ai_enabled` | `true` | yes | Master switch for AI seeding, simulation, replication and AI packets. |
| `ai_online_radius_m` | `220` | yes | Legacy alias. When set to a non-220 value it pins both hysteresis radii (no hysteresis); otherwise the enter/leave defaults apply. |
| `ai_enter_radius_m` | `180` | yes | A puppet enters a client's replication stream inside this 2D radius. |
| `ai_leave_radius_m` | `220` | yes | A visible puppet leaves only past this radius (hysteresis). Clamped ≥ enter radius. |
| `ai_max_entities` | `64` | yes | Maximum registered puppet squads; registrations beyond the cap are logged and skipped, and per-session in-range sets are capped nearest-first. |
| `ai_combat_enabled` | `true` | yes | Master switch for the puppet combat FSM; `false` keeps patrol replication but disables aggro, chase and attacks. |
| `ai_aggro_radius_m` | `40` | yes | 3D radius in which a puppet acquires a hostile player. |
| `ai_attack_range_m` | `2.0` | yes | Melee reach; inside this distance the FSM starts swinging. |
| `ai_attack_cooldown_ms` | `1500` | yes | Milliseconds between melee swings. |
| `ai_melee_damage` | `10` | yes | Damage per melee swing. |
| `ai_corpse_seconds` | `5` | yes | How long a dead puppet streams the death animation before `ENTITY_LEAVE_AOI`. |
| `ai_patrol_resume_s` | `10` | yes | ALERT cool-down before the puppet resumes patrol after losing its target. |
| `los_enabled` | `true` | yes | Master switch for server-side line-of-sight hit gating; `false` allows every hit. |
| `los_data_dir` | `"zone_los"` | yes | Directory holding the per-level `<level>.occl` occluders next to the server binary. Missing files fail open. |
| `world_item_ttl_min` | `60` (pointer; explicit `0` disables) | yes | Minutes before dropped world items expire via the 30 s sweep. |
| `world_item_max_per_level` | `500` | yes | Max persisted world item rows per level; drops beyond the cap are rejected and the client is resynced. |
| `trade_max_money_delta` | `200000` | yes | Maximum rubles one `OpTradeAction` may move; a capped buy/sell is echoed as corrected. |

### Occluders (`zone_los`)

Line-of-sight gating needs a `<level>.occl` file per level. Release packages are expected to ship `server/zone_los/l01_escape.occl` (52,967,834 bytes) next to `zone-server.exe`; the artifact is not tracked in git and is not present in the in-tree v0.6.0 snapshot. To generate one from a level's `level.cform`:

```bat
levelgeom.exe --cform <path>\level.cform --out zone_los\l01_escape.occl
```

Place every generated `.occl` in the configured `los_data_dir` (default `zone_los`, resolved relative to the working directory). A missing, unreadable or corrupt file fails **open** — hits are allowed and one warning is logged per level — so only levels with an artifact get geometry gating.

### Level travel

The menu start is always `l01_escape` (engine limitation). Once in game, the client reports every level load with `OpLevelChange` (0x0074); the server accepts any printable level name, resets the session's grid/AoI/AI caches, exchanges entity enters/leaves with peers on both levels, recomputes safe-zone state and re-sends world items in range. No reconnect is needed. See [zone-server/docs/LEVEL_TRAVEL.md](zone-server/docs/LEVEL_TRAVEL.md).

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
5. In game, the HUD shows `[ZO] Online` with the current ping. Remote players appear as proxies; AI patrol squads appear near the Cordon and may engage hostile players.

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

A new account receives `SHOW_START` (0x0071) and the native faction/loadout dialog opens. Pick a faction and loadout and click **Start**: the server validates the choice, creates the character plus starter inventory in SQLite, then the client starts `l01_escape`, applies the server spawn/faction and materializes the synced inventory. Returning accounts go straight to their saved spawn. The menu start is always `l01_escape` (engine limitation); in game, changing level reports `OpLevelChange` and the server switches peers, safe-zone state and per-level caches without a reconnect.

### Factions, groups and chat

- Faction relations are server-authoritative and ship to clients as `configs/mod_system_zone_faction_relations.ltx`; hostile factions are flagged as enemies, and the server rejects same-faction and same-group damage. Movement is client-authoritative by owner decision: the server accepts finite, in-bounds transforms as-is with no speed/teleport validation or corrections. Player-vs-player hits are validated: the client reports a proxy hit with `OpDamageNotify` (0x0050), the server checks attacker session, cross-level, safe zones on either side, friendly fire, 3D range (≤ 300 m), damage sanity, a 150 single-hit clamp, the rolling damage budget and — when an occluder exists for the level — the multi-sample line-of-sight gate, then relays the accepted hit to the victim, whose client applies it via `change_health`. The path is implemented but not yet exercised by two live clients, and damage is applied 1:1 with no server-side armor scaling.
- Mixed-faction groups are supported. Chat commands: `/invite <callsign>`, `/accept`, `/decline`, `/leave`, `/group`.
- Open chat with the game's `kXRR_CHAT` binding when the executable exposes it, otherwise `Y`.

### Safe zones

Twelve canonical zones (Rookie Village, 100 Rads Bar, Yantar Bunker, Flea Market, etc.) protect every faction equally: weapons are holstered, fire is blocked, damage is suppressed in both directions and client-side AI hostility is suppressed, with a PDA notification and HUD banner on entry/leave. Safe-zone protection does not depend on faction, group or war state.

### Item ledger, world items, stashes and traders

Dropping, picking up and consuming items are server-authoritative (0x007D/0x007E): the server owns the inventory rows and world item ids, rejects duplicate pickups and rate-limits actions. Dropped items persist in the world, sync to players entering the area and expire after the configured TTL. The native stash and trader windows are server-authoritative too: stash store/take (0x0081/0x0082) addresses a world stash by level position (0.5 m grid, 5 m reach, created on first store), trader buy/sell (0x0083/0x0084) debits or credits the SQLite ruble balance with the price capped by `trade_max_money_delta`, and every money change pushes `OpWalletUpdate` (0x0085) so the client wallet reconciles. The numeric container path (0x007F/0x0080, SQLite rowid addressing) is implemented and tested server-side, but no client container UI drives it yet.

### AI patrols and combat

Server-authoritative squads replicate to clients inside 180 m (leaving past 220 m) and stream state at ~30 Hz. With `ai_combat_enabled` (default true) a hostile squad acquires players inside 40 m, chases at run speed, melees at 2 m reach every 1.5 s, and despawns 5 s after death; hostility follows the faction relation matrix, monsters engage everyone, and safe zones suppress the FSM on both sides. Actor fire damages a puppet over the same 0x0050 path (`TargetID >= 1_000_000`). Puppets do not loot and do not path around level geometry yet.

### Level travel

Once a level is loaded, travel to any installed level works: the client loads it and reports `OpLevelChange` (0x0074). The server accepts the report, resets the session's spatial-grid/AoI/AI caches, exchanges entity enters/leaves between the old and new level, recomputes safe-zone state from the accepted position and re-sends world items in range. The menu start remains `l01_escape` in this release.

---

## Troubleshooting

- **`zone_identity.ltx.tmp` orphan:** a harmless leftover from an interrupted atomic write. Both writers (C++ identity and Lua favorites) clean stale `.tmp` files and preserve each other's sections; deleting one by hand is safe.
- **Stale identity / favorites:** delete `<Anomaly>\appdata\zone_identity.ltx` only as a last resort; it also resets favorites and history.
- **Server does not appear in LAN scan:** Windows Firewall may be blocking UDP `27015`; allow the server executable or connect by IP directly.
- **Port conflict:** change `port` in `zone_server.yaml` or pass `--port`, and use the same port in the browser.
- **AI absent:** check `ai_enabled: true` and that the `ai_squads` table has rows (it is seeded when empty).
- **AI attacks you near the Cordon:** expected when your faction is hostile to the squad (`ai_combat_enabled: true`). Set `ai_combat_enabled: false` to keep patrols but disable aggro. Safe zones suppress attacks on both sides.
- **Hits rejected as "no line of sight":** the level has an occluder and all three body samples were blocked. This is working as intended; remove or rename the level's `.occl` only for debugging. A missing occluder fails open and never blocks damage.
- **Occluder not used:** confirm `los_enabled: true`, the level has a matching `<level>.occl` in `los_data_dir`, and the server's working directory is where the `zone_los` folder lives. Names are case-sensitive on Linux; one warning per level is logged when loading fails.
- **Items not dropping/picking up:** item actions are gated on being connected and outside an xrRazom session; watch for the resync notice if the server rejects an action.
- **Stash/trade action rejected:** both share the item rate limit and the client-monotonic ActionID floor with the item ledger. A rejected action resyncs inventory and wallet; a trader price above `trade_max_money_delta` (200000) is applied as a corrected, smaller delta. Stashes must be within 5 m of your server position and on the same level.
