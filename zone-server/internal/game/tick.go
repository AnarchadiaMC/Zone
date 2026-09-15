package game

import (
	"context"
	"time"
)

// RunGameLoop runs the 30Hz game tick and integrates with Server.
// PRODUCTION FIX: single retransmit path via Server.Tick -> ackQueue.Tick
// (sendFunc + maxRetries + onDrop). The old second 100ms RetransmitExpired
// ticker doubled reliable traffic and retried unbounded (no maxRetries).
// Tick rate honors cfg.TickRateHz instead of hardcoded 30Hz.
func (s *Server) RunGameLoop(ctx context.Context) {
	interval := 33333 * time.Microsecond // 30 Hz default
	if s.cfg != nil && s.cfg.TickRateHz > 0 {
		interval = time.Second / time.Duration(s.cfg.TickRateHz)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.Tick(now)
		}
	}
}
