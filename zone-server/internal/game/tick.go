package game

import (
	"context"
	"time"
)

// RunGameLoop runs the 30Hz game tick and integrates with Server.
func (s *Server) RunGameLoop(ctx context.Context) {
	ticker := time.NewTicker(33333 * time.Microsecond) // 30 Hz
	defer ticker.Stop()
	retransmitTicker := time.NewTicker(100 * time.Millisecond)
	defer retransmitTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.Tick(now)
		case now := <-retransmitTicker.C:
			if udp := s.GetUDP(); udp != nil {
				s.ackQueue.RetransmitExpired(now, udp)
			}
		}
	}
}
