package game

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultAdminPipe = `\\.\pipe\zone_admin`

type adminListener interface {
	Accept() (io.ReadWriteCloser, error)
	Close() error
}

type AdminServer struct {
	server   *Server
	pipeName string
	running  atomic.Bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	listener adminListener
}

func NewAdminServer(s *Server, pipeName ...string) *AdminServer {
	name := DefaultAdminPipe
	if len(pipeName) > 0 && pipeName[0] != "" {
		name = pipeName[0]
	} else if s != nil && s.cfg != nil && s.cfg.AdminPipe != "" {
		name = s.cfg.AdminPipe
	}
	return &AdminServer{
		server:   s,
		pipeName: name,
	}
}

func (a *AdminServer) PipeName() string {
	return a.pipeName
}

func (a *AdminServer) IsRunning() bool {
	return a.running.Load()
}

func (a *AdminServer) ExecuteCommand(cmdLine string) string {
	cmdLine = strings.TrimSpace(cmdLine)
	if cmdLine == "" {
		return ""
	}

	parts := strings.SplitN(cmdLine, " ", 2)
	cmd := strings.ToLower(parts[0])
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	if a.server == nil {
		return "ERR server not initialized\n"
	}

	switch cmd {
	case "status":
		active, tickRate, uptime := a.server.GetStats()
		return fmt.Sprintf("OK active_sessions=%d tick_rate=%d uptime=%s\n", active, tickRate, uptime.Round(time.Second))

	case "kick":
		if args == "" {
			return "ERR usage: kick <session_id>\n"
		}
		sessID, err := strconv.ParseUint(args, 10, 32)
		if err != nil {
			return "ERR invalid session_id\n"
		}
		if a.server.KickSession(uint32(sessID)) {
			return fmt.Sprintf("OK session %d kicked\n", sessID)
		}
		return fmt.Sprintf("ERR session %d not found\n", sessID)

	case "ban":
		if args == "" {
			return "ERR usage: ban <uuid> <reason>\n"
		}
		banParts := strings.SplitN(args, " ", 2)
		uuid := banParts[0]
		reason := "banned by admin"
		if len(banParts) > 1 && strings.TrimSpace(banParts[1]) != "" {
			reason = strings.TrimSpace(banParts[1])
		}
		if err := a.server.BanPlayer(uuid, reason); err != nil {
			return fmt.Sprintf("ERR failed to ban %s: %v\n", uuid, err)
		}
		return fmt.Sprintf("OK account %s banned: %s\n", uuid, reason)

	case "broadcast":
		if args == "" {
			return "ERR usage: broadcast <message>\n"
		}
		a.server.BroadcastChat(0, args)
		return fmt.Sprintf("OK broadcast sent: %s\n", args)

	default:
		return fmt.Sprintf("ERR unknown command: %s\n", cmd)
	}
}

func (a *AdminServer) Start(ctx context.Context) error {
	if a.running.Swap(true) {
		return errors.New("admin server already running")
	}

	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	l, err := newNamedPipeListener(a.pipeName)
	if err != nil {
		a.running.Store(false)
		return err
	}
	a.listener = l

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for a.running.Load() {
			conn, err := a.listener.Accept()
			if err != nil {
				if !a.running.Load() {
					return
				}
				continue
			}
			go a.handleConnection(conn)
		}
	}()

	go func() {
		<-ctx.Done()
		a.Stop()
	}()

	return nil
}

func (a *AdminServer) Stop() {
	if !a.running.Swap(false) {
		return
	}
	if a.cancel != nil {
		a.cancel()
	}
	if a.listener != nil {
		_ = a.listener.Close()
	}
	a.wg.Wait()
}

func (a *AdminServer) handleConnection(conn io.ReadWriteCloser) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		resp := a.ExecuteCommand(line)
		if resp != "" {
			_, _ = conn.Write([]byte(resp))
		}
	}
}
