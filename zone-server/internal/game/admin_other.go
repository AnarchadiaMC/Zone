//go:build !windows

package game

import (
	"io"
	"net"
	"os"
)

type otherPipeListener struct {
	listener net.Listener
}

func newNamedPipeListener(pipeName string) (adminListener, error) {
	sockPath := "/tmp/zone_admin.sock"
	_ = os.Remove(sockPath)
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	return &otherPipeListener{listener: l}, nil
}

func (l *otherPipeListener) Accept() (io.ReadWriteCloser, error) {
	conn, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (l *otherPipeListener) Close() error {
	return l.listener.Close()
}
