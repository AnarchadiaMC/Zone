//go:build windows

package game

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

var (
	modkernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procDisconnectNamedPipe = modkernel32.NewProc("DisconnectNamedPipe")
)

func disconnectNamedPipe(h windows.Handle) {
	_, _, _ = procDisconnectNamedPipe.Call(uintptr(h))
}

type windowsPipeListener struct {
	pipeName string
	closed   atomic.Bool
	mu       sync.Mutex
	curPipe  windows.Handle
}

type windowsPipeConn struct {
	handle windows.Handle
}

func (c *windowsPipeConn) Read(b []byte) (int, error) {
	var done uint32
	err := windows.ReadFile(c.handle, b, &done, nil)
	if err != nil {
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
			return int(done), io.EOF
		}
		return int(done), err
	}
	return int(done), nil
}

func (c *windowsPipeConn) Write(b []byte) (int, error) {
	var done uint32
	err := windows.WriteFile(c.handle, b, &done, nil)
	if err != nil {
		return int(done), err
	}
	return int(done), nil
}

func (c *windowsPipeConn) Close() error {
	disconnectNamedPipe(c.handle)
	_ = windows.CloseHandle(c.handle)
	return nil
}

func newNamedPipeListener(pipeName string) (adminListener, error) {
	if pipeName == "" {
		pipeName = DefaultAdminPipe
	}
	return &windowsPipeListener{
		pipeName: pipeName,
	}, nil
}

func (l *windowsPipeListener) createInstance() (windows.Handle, error) {
	pipePath, err := windows.UTF16PtrFromString(l.pipeName)
	if err != nil {
		return windows.InvalidHandle, err
	}

	h, err := windows.CreateNamedPipe(
		pipePath,
		windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES,
		4096,
		4096,
		0,
		nil,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return h, nil
}

func (l *windowsPipeListener) Accept() (io.ReadWriteCloser, error) {
	if l.closed.Load() {
		return nil, net.ErrClosed
	}

	hPipe, err := l.createInstance()
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.curPipe = hPipe
	l.mu.Unlock()

	err = windows.ConnectNamedPipe(hPipe, nil)
	if err != nil && err != windows.ERROR_PIPE_CONNECTED {
		_ = windows.CloseHandle(hPipe)
		if l.closed.Load() || errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
			return nil, net.ErrClosed
		}
		return nil, err
	}

	if l.closed.Load() {
		disconnectNamedPipe(hPipe)
		_ = windows.CloseHandle(hPipe)
		return nil, net.ErrClosed
	}

	return &windowsPipeConn{
		handle: hPipe,
	}, nil
}

func (l *windowsPipeListener) Close() error {
	if l.closed.Swap(true) {
		return nil
	}

	l.mu.Lock()
	if l.curPipe != windows.InvalidHandle && l.curPipe != 0 {
		_ = windows.CancelIoEx(l.curPipe, nil)
		_ = windows.CloseHandle(l.curPipe)
		l.curPipe = windows.InvalidHandle
	}
	l.mu.Unlock()

	return nil
}
