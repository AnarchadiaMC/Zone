//go:build !windows

package game

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type otherPipeListener struct {
	listener net.Listener
	sockPath string
}

func resolveSocketPath(pipeName string) string {
	if pipeName == "" {
		return "/tmp/zone_admin.sock"
	}
	cleanName := strings.TrimPrefix(pipeName, `\\.\pipe\`)
	cleanName = strings.TrimPrefix(cleanName, `//./pipe/`)
	if strings.HasPrefix(cleanName, "/") {
		if !strings.HasSuffix(cleanName, ".sock") {
			return cleanName + ".sock"
		}
		return cleanName
	}
	cleanName = strings.TrimSuffix(cleanName, ".sock")
	return filepath.Join("/tmp", cleanName+".sock")
}

func newNamedPipeListener(pipeName string) (adminListener, error) {
	sockPath := resolveSocketPath(pipeName)
	_ = os.Remove(sockPath)
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sockPath, 0600); err != nil {
		_ = l.Close()
		_ = os.Remove(sockPath)
		return nil, err
	}
	return &otherPipeListener{
		listener: l,
		sockPath: sockPath,
	}, nil
}

func (l *otherPipeListener) Accept() (io.ReadWriteCloser, error) {
	conn, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (l *otherPipeListener) Close() error {
	err := l.listener.Close()
	if l.sockPath != "" {
		_ = os.Remove(l.sockPath)
	}
	return err
}
