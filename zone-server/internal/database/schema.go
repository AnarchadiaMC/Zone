package database

const SchemaSQL = `
CREATE TABLE IF NOT EXISTS accounts (
    client_uuid TEXT PRIMARY KEY,
    hwid_hash TEXT NOT NULL,
    nickname TEXT NOT NULL UNIQUE,
    banned INTEGER NOT NULL DEFAULT 0,
    ban_reason TEXT,
    created_at INTEGER NOT NULL,
    last_seen INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS characters (
    client_uuid TEXT PRIMARY KEY REFERENCES accounts(client_uuid) ON DELETE CASCADE,
    level_name TEXT NOT NULL DEFAULT 'l01_escape',
    pos_x REAL NOT NULL DEFAULT -211.3,
    pos_y REAL NOT NULL DEFAULT -20.2,
    pos_z REAL NOT NULL DEFAULT -145.8,
    yaw REAL NOT NULL DEFAULT 0.0,
    faction TEXT NOT NULL DEFAULT 'stalker',
    health REAL NOT NULL DEFAULT 1.0,
    bleeding REAL NOT NULL DEFAULT 0.0,
    radiation REAL NOT NULL DEFAULT 0.0,
    economy_tier INTEGER NOT NULL DEFAULT 1,
    rank TEXT NOT NULL DEFAULT 'novice',
    reputation INTEGER NOT NULL DEFAULT 0,
    rubles INTEGER NOT NULL DEFAULT 5000,
    play_time_sec INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS character_inventory (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    client_uuid TEXT NOT NULL REFERENCES characters(client_uuid) ON DELETE CASCADE,
    item_section TEXT NOT NULL,
    condition REAL NOT NULL DEFAULT 1.0,
    ammo_current INTEGER NOT NULL DEFAULT 0,
    addon_flags INTEGER NOT NULL DEFAULT 0,
    slot INTEGER NOT NULL DEFAULT -1,
    grid_x INTEGER NOT NULL DEFAULT 0,
    grid_y INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS world_stashes (
    stash_id INTEGER PRIMARY KEY AUTOINCREMENT,
    level_name TEXT NOT NULL,
    pos_x REAL NOT NULL,
    pos_y REAL NOT NULL,
    pos_z REAL NOT NULL,
    owner_uuid TEXT,
    passcode TEXT,
    contents_json TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS safe_zones (
    zone_id TEXT PRIMARY KEY,
    level_name TEXT NOT NULL,
    shape TEXT NOT NULL DEFAULT 'cylinder',
    center_x REAL NOT NULL,
    center_y REAL NOT NULL,
    center_z REAL NOT NULL,
    radius REAL NOT NULL,
    height REAL NOT NULL DEFAULT 20.0,
    description TEXT
);
CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp INTEGER NOT NULL,
    client_uuid TEXT,
    event_type TEXT NOT NULL,
    detail TEXT
);
CREATE TABLE IF NOT EXISTS ai_squads (
    squad_id INTEGER PRIMARY KEY AUTOINCREMENT,
    level_name TEXT NOT NULL,
    section TEXT NOT NULL,
    faction TEXT NOT NULL,
    pos_x REAL NOT NULL,
    pos_y REAL NOT NULL,
    pos_z REAL NOT NULL,
    patrol_path TEXT,
    is_online INTEGER NOT NULL DEFAULT 0
);
-- Performance indexes (idempotent; safe for existing DBs on Open).
-- NOTE: this schema uses client_uuid as the account/character key, so
-- idx_char_account / idx_inv_char target client_uuid (spec names kept).
CREATE INDEX IF NOT EXISTS idx_char_account ON characters(client_uuid);
CREATE INDEX IF NOT EXISTS idx_inv_char ON character_inventory(client_uuid);
CREATE INDEX IF NOT EXISTS idx_stashes_level ON world_stashes(level_name);
CREATE INDEX IF NOT EXISTS idx_zones_level ON safe_zones(level_name);
`
