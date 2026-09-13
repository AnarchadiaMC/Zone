package network

import (
	"net"
	"testing"
)

func BenchmarkSessionManager_GetAll(b *testing.B) {
	sm := NewSessionManager()
	for i := 0; i < 1000; i++ {
		sm.AddSession(&PlayerSession{
			SessionID: uint32(i),
			UDPAddr:   &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: i},
		})
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		all := sm.GetAll()
		_ = all
	}
}
