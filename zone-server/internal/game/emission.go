package game

import (
	"bytes"
	"sync/atomic"
	"time"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// EmissionState represents the current phase of a Zone-wide emission event.
type EmissionState uint8

const (
	// EmissionDormant is the quiet period between emissions.
	EmissionDormant EmissionState = 0
	// EmissionWarning is broadcast ~60 seconds before the wave hits — seek shelter.
	EmissionWarning EmissionState = 1
	// EmissionActive is the lethal emission wave itself.
	EmissionActive EmissionState = 2
	// EmissionClear signals the all-clear after the wave has passed.
	EmissionClear EmissionState = 3
)

// emissionEventType is the EventType value written into WorldEventPayload for emission events.
const emissionEventType uint8 = 0

// EmissionOrchestrator manages Zone-wide emission events and drives the
// Dormant -> Warning -> Active -> Clear -> Dormant state machine.
type EmissionOrchestrator struct {
	state    EmissionState
	nextTick time.Time

	// Configurable phase durations (defaults set by NewEmissionOrchestrator).
	dormantDuration time.Duration // time between emissions (default: 30 min)
	warningDuration time.Duration // pre-emission warning window (default: 60 s)
	activeDuration  time.Duration // lethal wave duration (default: 120 s)
	clearDuration   time.Duration // all-clear window (default: 30 s)
}

// NewEmissionOrchestrator creates an orchestrator in the Dormant state with
// production-ready default durations. The first emission warning will fire
// after dormantDuration elapses from server start.
func NewEmissionOrchestrator() *EmissionOrchestrator {
	return &EmissionOrchestrator{
		state:           EmissionDormant,
		nextTick:        time.Now().Add(30 * time.Minute),
		dormantDuration: 30 * time.Minute,
		warningDuration: 60 * time.Second,
		activeDuration:  120 * time.Second,
		clearDuration:   30 * time.Second,
	}
}

// CurrentState returns the current emission state.
func (e *EmissionOrchestrator) CurrentState() EmissionState {
	return e.state
}

// Tick advances the emission state machine. It must be called on every server
// tick. When a state transition occurs the new state is broadcast to all
// connected sessions via OpWorldEvent (0x0030).
//
// sessions and udp may be nil (e.g. in unit tests) -- broadcasts are skipped
// gracefully in that case.
func (e *EmissionOrchestrator) Tick(now time.Time, sessions *network.SessionManager, udp *network.UDPListener, seq *atomic.Uint32) {
	if now.Before(e.nextTick) {
		return
	}

	// Advance to the next state.
	var nextDuration time.Duration
	switch e.state {
	case EmissionDormant:
		e.state = EmissionWarning
		nextDuration = e.warningDuration
	case EmissionWarning:
		e.state = EmissionActive
		nextDuration = e.activeDuration
	case EmissionActive:
		e.state = EmissionClear
		nextDuration = e.clearDuration
	case EmissionClear:
		e.state = EmissionDormant
		nextDuration = e.dormantDuration
	}
	e.nextTick = now.Add(nextDuration)

	e.broadcast(sessions, udp, seq, uint32(nextDuration.Seconds()))
}

// broadcast sends a WorldEventPayload to all connected sessions.
func (e *EmissionOrchestrator) broadcast(
	sessions *network.SessionManager,
	udp *network.UDPListener,
	seq *atomic.Uint32,
	timerSec uint32,
) {
	if sessions == nil || udp == nil || seq == nil {
		return
	}

	pkt := protocol.WorldEventPayload{
		EventType: emissionEventType,
		State:     uint8(e.state),
		Timer:     timerSec,
	}

	for _, sess := range sessions.GetAll() {
		if sess == nil || sess.UDPAddr == nil {
			continue
		}

		s := seq.Add(1)
		var buf bytes.Buffer
		if err := protocol.WritePacket(&buf, protocol.OpWorldEvent, s, protocol.FlagReliable, pkt); err != nil {
			continue
		}
		_ = udp.Send(sess.UDPAddr, buf.Bytes())
	}
}
