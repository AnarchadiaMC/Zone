package game

import (
	"crypto/rand"
	"encoding/binary"
	"time"

	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
)

// GenerateSessionToken generates a cryptographically secure, non-zero 64-bit session token.
func GenerateSessionToken() (uint64, error) {
	for {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		token := binary.LittleEndian.Uint64(b[:])
		if token != 0 {
			return token, nil
		}
	}
}

// CheckPlayerBanned checks if a player with client_uuid is banned in the database.
// Returns (isBanned, banReason, err). If db == nil, returns (false, "", nil). Calls db.IsPlayerBanned(uuid).
func CheckPlayerBanned(db *database.DB, uuid string) (bool, string, error) {
	if db == nil {
		return false, "", nil
	}
	return db.IsPlayerBanned(uuid)
}

// AuthenticateSessionPacket verifies packet authentication against a session.
// Verifies sess != nil && sess.SessionID == expectedSessionID.
// If token != 0 && sess.SessionToken != 0, checks token == sess.SessionToken.
func AuthenticateSessionPacket(sess *network.PlayerSession, expectedSessionID uint32, token uint64) bool {
	if sess == nil || sess.SessionID != expectedSessionID {
		return false
	}
	sess.Lock()
	sessToken := sess.SessionToken
	sess.Unlock()

	if token != 0 && sessToken != 0 {
		return token == sessToken
	}
	return true
}

// ShouldCheckpointSession evaluates whether a session needs persistence checkpointing.
// Returns true if sess != nil and sess.Dirty and now.Sub(sess.LastCheckpoint) >= interval.
func ShouldCheckpointSession(sess *network.PlayerSession, now time.Time, interval time.Duration) bool {
	if sess == nil {
		return false
	}
	sess.Lock()
	defer sess.Unlock()
	return sess.Dirty && now.Sub(sess.LastCheckpoint) >= interval
}
