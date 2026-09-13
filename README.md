# Zone Online — S.T.A.L.K.E.R. Anomaly Multiplayer

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![C++ Standard](https://img.shields.io/badge/C%2B%2B-17-00599C?style=flat&logo=c%2B%2B)](https://isocpp.org)
[![Target Engine](https://img.shields.io/badge/Engine-Anomaly%201.5.3%20Modded%20EXEs-orange.svg)](https://github.com/themrdemonized/STALKER-Anomaly-modded-exes)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Zone Online** is a next-generation dedicated survival multiplayer architecture for **S.T.A.L.K.E.R. Anomaly 1.5.3**. It pairs a high-performance, authoritative Golang server with an autonomous, injectable C++ client DLL and native X-Ray UI.

---

## Architectural Overview

```
                      ┌─────────────────────────────────────────┐
                      │        Embedded SQLite Database         │
                      │             (zone_world.db)             │
                      │   (WAL Mode, Zero-Config, Single File)  │
                      └────────────────────▲────────────────────┘
                                           │ SQL (Async Write-Behind)
                      ┌────────────────────▼────────────────────┐
                      │        Dedicated Golang Server          │
                      │             (zone-server)               │
                      │ ┌─────────────────────────────────────┐ │
                      │ │ 30Hz Fixed Ticker / Spatial Grid    │ │
                      │ │ Safe Zone Boundary Check & Raycast  │ │
                      │ │ Persistent HWID/UUID Account Engine │ │
                      │ │ Mutant Raids / Emission Orchestrator│ │
                      │ │ Zero-Alloc UDP Buffer Pool          │ │
                      │ └─────────────────────────────────────┘ │
                      └──────────────▲───────────────▲──────────┘
            Binary UDP (Packets)     │               │     Binary UDP (Packets)
        ┌────────────────────────────┘               └───────────────────────────┐
        │                                                                        │
┌───────┴───────────────────────────┐                    ┌───────────────────────┴───────────────────────────┐
│         Client Machine A          │                    │         Client Machine B          │
│ ┌───────────────────────────────┐ │                    │ ┌───────────────────────────────┐ │
│ │ AnomalyDX11.exe               │ │                    │ │ AnomalyDX11.exe               │ │
│ │   ├─ ZoneClient.dll           │ │                    │ │   ├─ ZoneClient.dll           │ │
│ │   │    ├─ C++ UDP Thread      │ │                    │ │   │    ├─ C++ UDP Thread      │ │
│ │   │    ├─ Asset Provisioner   │ │                    │ │   │    ├─ Asset Provisioner   │ │
│ │   │    ├─ HWID/UUID Identity  │ │                    │ │   │    ├─ HWID/UUID Identity  │ │
│ │   │    └─ MinHook / LuaJIT    │ │                    │ │   │    └─ MinHook / LuaJIT    │ │
│ │   └─ Native Lua Layer         │ │                    │ │   └─ Native Lua Layer         │ │
│ │        ├─ Menu Button & Browser│ │                   │ │        ├─ Menu Button & Browser│ │
│ │        ├─ Safe Zone Enforcer  │ │                    │ │        ├─ Safe Zone Enforcer  │ │
│ │        └─ Hermite Spline Proxy│ │                    │ │        └─ Hermite Spline Proxy│ │
│ └───────────────────────────────┘ │                    │ └───────────────────────────────┘ │
└───────────────────────────────────┘                    └───────────────────────────────────┘
```

---

## Key Highlights

- **Headless Authoritative Daemon (`zone-server`)**: Written in Go for native concurrency and low memory footprint. Ticks at a fixed 30 Hz ($33.3\text{ ms}$), managing 64m spatial grid partitioning, 220m Area of Interest (AoI), emissions, and two-tier AI simulation.
- **Embedded SQLite Persistence**: Zero external DB dependencies (Postgres/Redis/Docker). Operates in Write-Ahead Logging (WAL) mode with async write-behind queues.
- **Autonomous Zero-Touch Client (`ZoneClient.dll`)**: Injected into Anomaly via `ZoneClient_Injector.exe`. Contains an embedded `AssetProvisioner` that auto-provisions DLTX configs (`mod_system_zone_online.ltx`) and UI layouts (`zone_ui_server_list.xml`) on attach.
- **Native X-Ray UI**: Zero external overlay layers (no Dear ImGui) or DirectX Present hooks. Injects a native 3-state button (`CUI3tButton`) on the main menu, opening a native `CUIScriptWnd` Server Browser with Direct Connect and persistent Favorites.
- **Cubic Hermite Spline Interpolation**: Remote stalker proxies are smoothed using cubic Hermite splines with a 100ms jitter buffer, eliminating stuttering and rubberbanding.
- **Cylindrical Safe Zones**: 8 canonical Zone locations (Rookie Village, 100 Rads Bar, Yantar Bunker, etc.) enforce weapon holstering, godmode protection, and PDA alerts.

---

## Repository Structure

```
.
├── zone-server/           # Golang dedicated survival server
│   ├── cmd/server/        # Server entrypoint (main.go)
│   ├── internal/ai/       # Two-tier AI simulation & A* pathfinding
│   ├── internal/config/   # YAML configuration loader
│   ├── internal/database/ # SQLite schema (7 tables) & CRUD operations
│   ├── internal/game/     # 30Hz ticker, spatial grid, safe zones, AoI, events
│   ├── internal/network/  # UDP listener, worker pool, reliable ACK queue, sessions
│   └── internal/protocol/ # Binary wire protocol (16 opcodes, 12-byte header)
├── zone-client/           # C++ injectable client DLL & automated injector
│   ├── 3rdparty/          # MinHook source & dynamic LuaJIT headers
│   ├── injector/          # Win32 injector with --launch, --wait, --pid modes
│   └── src/               # DllMain, AssetProvisioner, UDP thread, Lua bindings
├── gamedata/              # Native X-Ray configs & scripts for Anomaly 1.5.3
│   ├── configs/           # DLTX mod_system_zone_online.ltx & UI XML layouts
│   └── scripts/           # Native Lua scripts (menu patch, browser, HUD, proxy)
├── Makefile               # Root build orchestrator
├── INSTALL.md             # Detailed installation and setup guide
├── WALKTHROUGH.md         # Comprehensive architectural walkthrough
└── README.md              # Project documentation
```

---

## Binary UDP Protocol Specification

All communication occurs over UDP port `27015` using packed little-endian binary structures:

### 12-Byte Header Format
```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|       Magic (0x5A4F "ZO")     |  Protocol(1)  |  Flags/Chan   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    Sequence Number (uint32)                   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|        Opcode (uint16)        |     Payload Length (uint16)   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

### Core Opcodes
| Opcode | Name | Direction | Reliability | Payload Description |
|:---|:---|:---|:---|:---|
| `0x0001` | `PKT_HANDSHAKE_REQ` | C → S | Reliable | UUID (36B), HWID Hash (4B), Nickname (32B), ProtoVer (1B) |
| `0x0002` | `PKT_HANDSHAKE_RES` | S → C | Reliable | SessionID (4B), Status (1B), SpawnX/Y/Z (12B), WorldTime (8B), EcoTier (1B) |
| `0x0003` | `PKT_DISCONNECT` | Both | Reliable | Reason (1B) |
| `0x0004` | `PKT_HEARTBEAT` | Both | Unreliable | Timestamp (8B) |
| `0x0010` | `PKT_CLIENT_TRANSFORM` | C → S | Unreliable | SessionID (4B), PosX/Y/Z (12B), Yaw/Pitch (4B), VelX/Y/Z (6B), AnimFlags (2B) |
| `0x0011` | `PKT_SERVER_SNAPSHOT` | S → C | Unreliable | Peer Count (1B), Array of: SessionID, PosX/Y/Z, Yaw/Pitch, AnimFlags, Health |
| `0x0012` | `PKT_ENTITY_ENTER_AOI` | S → C | Reliable | EntityID (4B), Type (1B), Section (32B), PosX/Y/Z (12B), Faction (1B), Health (2B) |
| `0x0013` | `PKT_ENTITY_LEAVE_AOI` | S → C | Reliable | EntityID (4B) |
| `0x0020` | `PKT_SAFEZONE_STATE` | S → C | Reliable | Locked (1B: 0=free, 1=locked), ZoneID (16B) |
| `0x0030` | `PKT_WORLD_EVENT` | S → C | Reliable | EventType (1B: Emission Warn/Active/Clear, Raid Start/End), Timer (4B) |
| `0x0040` | `PKT_STASH_INTERACT` | C → S | Reliable | StashID (4B), Action (1B: Open/Close/Take/Put), ItemSection (32B), Count (2B) |
| `0x0050` | `PKT_DAMAGE_NOTIFY` | S → C | Reliable | TargetSessionID (4B), AttackerSessionID (4B), Damage (4B float), BoneID (1B) |
| `0x0060` | `PKT_CHAT_TEXT` | Both | Reliable | SenderID (4B), Length (1B), Message Text (up to 255B) |
| `0x0070` | `PKT_AI_ACTION_EVENT` | S → C | Reliable | EntityID (4B), Action (1B: Idle/Patrol/Attack/Flee/Death), TargetID (4B) |

---

## Quickstart

### 1. Start the Server
```bat
cd zone-server
go build -o zone-server.exe ./cmd/server
zone-server.exe
```

### 2. Build the Client
```bat
cd zone-client
cmake -S . -B build -G "Visual Studio 17 2022" -A x64
cmake --build build --config Release
```

### 3. Launch & Connect
Launch Anomaly using the automated injector:
```bat
ZoneClient_Injector.exe --launch "C:\Anomaly\bin\AnomalyDX11.exe"
```
Once in the main menu:
1. Click the native **Zone Online** button below the menu options.
2. Enter the server IP (default `127.0.0.1`), port (`27015`), and your callsign.
3. Click **Connect**.

---

## License

This project is licensed under the MIT License. S.T.A.L.K.E.R. and X-Ray Engine are trademarks of GSC Game World.