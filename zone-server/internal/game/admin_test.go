//go:build windows

package game

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"go.uber.org/zap"
	"golang.org/x/sys/windows"
)

func setupTestServer(t *testing.T) (*Server, *database.DB) {
	t.Helper()
	cfg := &config.Config{
		Port:        27015,
		TickRateHz:  30,
		MaxPlayers:  64,
		AdminPipe:   `\\.\pipe\zone_admin_test`,
	}
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}

	logger := zap.NewNop()
	server := NewServer(cfg, db, logger)
	return server, db
}

func TestAdminServer_ExecuteCommand(t *testing.T) {
	server, db := setupTestServer(t)
	admin := NewAdminServer(server)

	t.Run("Status Command", func(t *testing.T) {
		resp := admin.ExecuteCommand("status")
		if !strings.HasPrefix(resp, "OK active_sessions=0") {
			t.Errorf("Unexpected status response: %q", resp)
		}
		if !strings.Contains(resp, "tick_rate=30") {
			t.Errorf("Expected tick_rate in response: %q", resp)
		}
	})

	t.Run("Kick Command", func(t *testing.T) {
		// Register a dummy session
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
		sess := &network.PlayerSession{
			SessionID: 42,
			AccountID: "user-42",
			UDPAddr:   addr,
			LastSeen:  time.Now(),
		}
		server.sessions.AddSession(sess)

		// Kick existing session
		resp := admin.ExecuteCommand("kick 42")
		if !strings.HasPrefix(resp, "OK session 42 kicked") {
			t.Errorf("Unexpected kick response: %q", resp)
		}
		if server.sessions.GetByID(42) != nil {
			t.Errorf("Session 42 was not removed after kick")
		}

		// Kick nonexistent session
		resp = admin.ExecuteCommand("kick 999")
		if !strings.HasPrefix(resp, "ERR session 999 not found") {
			t.Errorf("Unexpected kick response for nonexistent session: %q", resp)
		}

		// Kick without args
		resp = admin.ExecuteCommand("kick")
		if !strings.HasPrefix(resp, "ERR usage: kick") {
			t.Errorf("Unexpected response: %q", resp)
		}
	})

	t.Run("Ban Command", func(t *testing.T) {
		uuid := "banned-user-1"
		_ = db.AutoProvision(uuid, "hwid-1", "CheaterStalker")

		// Add active session for this user
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321}
		sess := &network.PlayerSession{
			SessionID: 99,
			AccountID: uuid,
			UDPAddr:   addr,
			LastSeen:  time.Now(),
		}
		server.sessions.AddSession(sess)

		resp := admin.ExecuteCommand("ban banned-user-1 Speed hacking detected")
		if !strings.HasPrefix(resp, "OK account banned-user-1 banned: Speed hacking detected") {
			t.Errorf("Unexpected ban response: %q", resp)
		}

		// Verify account is banned in DB
		banned, reason, err := db.IsPlayerBanned(uuid)
		if err != nil || !banned || reason != "Speed hacking detected" {
			t.Errorf("DB ban check failed: banned=%v, reason=%s, err=%v", banned, reason, err)
		}

		// Verify session was kicked
		if server.sessions.GetByID(99) != nil {
			t.Errorf("Session for banned user was not kicked")
		}

		// Ban without args
		resp = admin.ExecuteCommand("ban")
		if !strings.HasPrefix(resp, "ERR usage: ban") {
			t.Errorf("Unexpected response: %q", resp)
		}
	})

	t.Run("Broadcast Command", func(t *testing.T) {
		resp := admin.ExecuteCommand("broadcast Server rebooting in 5 minutes!")
		if !strings.HasPrefix(resp, "OK broadcast sent: Server rebooting in 5 minutes!") {
			t.Errorf("Unexpected broadcast response: %q", resp)
		}

		resp = admin.ExecuteCommand("broadcast")
		if !strings.HasPrefix(resp, "ERR usage: broadcast") {
			t.Errorf("Unexpected response: %q", resp)
		}
	})

	t.Run("Unknown Command", func(t *testing.T) {
		resp := admin.ExecuteCommand("invalid_cmd foo bar")
		if !strings.HasPrefix(resp, "ERR unknown command: invalid_cmd") {
			t.Errorf("Unexpected response: %q", resp)
		}
	})

	t.Run("Authentication Required for Privileged Commands", func(t *testing.T) {
		authedAdmin := NewAdminServer(server)
		authedAdmin.SetAuthToken("secret-admin-pass")

		// Status does not require auth
		statusResp := authedAdmin.ExecuteCommand("status")
		if !strings.HasPrefix(statusResp, "OK active_sessions=") {
			t.Errorf("Expected status to succeed without auth: %q", statusResp)
		}

		// Kick fails without auth
		kickResp := authedAdmin.ExecuteCommand("kick 42")
		if !strings.HasPrefix(kickResp, "ERR unauthorized") {
			t.Errorf("Expected kick to fail without auth: %q", kickResp)
		}

		// Ban fails without auth
		banResp := authedAdmin.ExecuteCommand("ban user-1 reason")
		if !strings.HasPrefix(banResp, "ERR unauthorized") {
			t.Errorf("Expected ban to fail without auth: %q", banResp)
		}

		// Broadcast fails without auth
		bcResp := authedAdmin.ExecuteCommand("broadcast hello")
		if !strings.HasPrefix(bcResp, "ERR unauthorized") {
			t.Errorf("Expected broadcast to fail without auth: %q", bcResp)
		}

		// Auth with wrong token fails
		authResp := authedAdmin.ExecuteCommand("auth wrong-token")
		if !strings.HasPrefix(authResp, "ERR invalid token") {
			t.Errorf("Expected auth failure on wrong token: %q", authResp)
		}

		// Auth with correct token succeeds
		authResp = authedAdmin.ExecuteCommand("auth secret-admin-pass")
		if !strings.HasPrefix(authResp, "OK authenticated") {
			t.Errorf("Expected auth success on correct token: %q", authResp)
		}

		// Now kick succeeds or returns not found (not unauthorized)
		kickResp = authedAdmin.ExecuteCommand("kick 99999")
		if strings.HasPrefix(kickResp, "ERR unauthorized") {
			t.Errorf("Expected kick to be authorized after auth: %q", kickResp)
		}
	})
}

func connectTestPipe(pipePath string) (io.ReadWriteCloser, error) {
	p, err := windows.UTF16PtrFromString(pipePath)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	return &windowsPipeConn{handle: h}, nil
}

func TestAdminServer_NamedPipeLifecycle(t *testing.T) {
	server, _ := setupTestServer(t)
	testPipe := fmt.Sprintf(`\\.\pipe\zone_admin_lifecycle_%d`, time.Now().UnixNano())
	admin := NewAdminServer(server, testPipe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := admin.Start(ctx); err != nil {
		t.Fatalf("AdminServer.Start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Connect as client to the pipe using Win32 client
	pipeClient, err := connectTestPipe(testPipe)
	if err != nil {
		t.Fatalf("Failed to open named pipe as client: %v", err)
	}

	// Send status command
	_, err = pipeClient.Write([]byte("status\n"))
	if err != nil {
		t.Fatalf("Failed to write to named pipe: %v", err)
	}

	buf := make([]byte, 256)
	n, readErr := pipeClient.Read(buf)
	if readErr != nil {
		t.Fatalf("Failed to read response from named pipe: %v", readErr)
	}
	resp := string(buf[:n])
	t.Logf("Read from pipe: %q", resp)
	if !strings.HasPrefix(resp, "OK active_sessions=") {
		t.Errorf("Unexpected response from pipe: %q", resp)
	}

	_ = pipeClient.Close()
	admin.Stop()

	if admin.IsRunning() {
		t.Errorf("AdminServer still reported running after Stop()")
	}
}
