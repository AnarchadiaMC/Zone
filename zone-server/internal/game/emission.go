package game

import (
	"bytes"
	"sync"
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

// Default phase durations.
const (
	DefaultDormantDuration = 30 * time.Minute
	DefaultWarningDuration = 60 * time.Second
	DefaultActiveDuration  = 120 * time.Second
	DefaultClearDuration   = 30 * time.Second
)

// EmissionOrchestrator manages Zone-wide emission events and drives the
// Dormant -> Warning -> Active -> Clear -> Dormant state machine.
// All access to internal state is synchronized via sync.RWMutex.
type EmissionOrchestrator struct {
	mu sync.RWMutex

	state    EmissionState
	nextTick time.Time

	// Configurable phase durations (defaults set by NewEmissionOrchestrator).
	dormantDuration time.Duration // time between emissions (default: 30 min)
	warningDuration time.Duration // pre-emission warning window (default: 60 s)
	activeDuration  time.Duration // lethal wave duration (default: 120 s)
	clearDuration   time.Duration // all-clear window (default: 30 s)

	forcePendingBroadcast bool
}

// NewEmissionOrchestrator creates an orchestrator in the Dormant state.
// If dormantDurations is provided and > 0, the first value is used as the
// dormant interval; otherwise, DefaultDormantDuration (30 min) is used.
func NewEmissionOrchestrator(dormantDurations ...time.Duration) *EmissionOrchestrator {
	dormant := DefaultDormantDuration
	if len(dormantDurations) > 0 && dormantDurations[0] > 0 {
		dormant = dormantDurations[0]
	}
	now := time.Now()
	return &EmissionOrchestrator{
		state:           EmissionDormant,
		nextTick:        now.Add(dormant),
		dormantDuration: dormant,
		warningDuration: DefaultWarningDuration,
		activeDuration:  DefaultActiveDuration,
		clearDuration:   DefaultClearDuration,
	}
}

// CurrentState returns the current emission state in a thread-safe manner.
func (e *EmissionOrchestrator) CurrentState() EmissionState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

// TimeUntilNext returns the duration until the next scheduled state transition.
// Returns 0 if the next tick time has already passed.
func (e *EmissionOrchestrator) TimeUntilNext() time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()
	rem := time.Until(e.nextTick)
	if rem < 0 {
		return 0
	}
	return rem
}

// SetDormantDuration updates the quiet period between emissions.
// If currently in EmissionDormant, it reschedules nextTick based on d.
func (e *EmissionOrchestrator) SetDormantDuration(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d <= 0 {
		return
	}
	e.dormantDuration = d
	if e.state == EmissionDormant {
		e.nextTick = time.Now().Add(d)
	}
}

// DormantDuration returns the configured dormant duration.
func (e *EmissionOrchestrator) DormantDuration() time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dormantDuration
}

// WarningDuration returns the configured warning duration.
func (e *EmissionOrchestrator) WarningDuration() time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.warningDuration
}

// ActiveDuration returns the configured active duration.
func (e *EmissionOrchestrator) ActiveDuration() time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activeDuration
}

// ClearDuration returns the configured clear duration.
func (e *EmissionOrchestrator) ClearDuration() time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.clearDuration
}

// SetWarningDuration updates the warning window duration.
func (e *EmissionOrchestrator) SetWarningDuration(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d > 0 {
		e.warningDuration = d
	}
}

// SetActiveDuration updates the lethal wave duration.
func (e *EmissionOrchestrator) SetActiveDuration(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d > 0 {
		e.activeDuration = d
	}
}

// SetClearDuration updates the all-clear window duration.
func (e *EmissionOrchestrator) SetClearDuration(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d > 0 {
		e.clearDuration = d
	}
}

// ForceEmission immediately triggers EmissionWarning for testing or admin commands.
// It sets the state to EmissionWarning, resets nextTick, and flags an immediate
// broadcast for the next Tick call.
func (e *EmissionOrchestrator) ForceEmission() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = EmissionWarning
	e.nextTick = time.Now().Add(e.warningDuration)
	e.forcePendingBroadcast = true
}

// Tick advances the emission state machine. It must be called on every server tick.
// On state transition, the new state is broadcast to all connected sessions via
// OpWorldEvent (0x0030) with WorldEventPayload{EventType: 0, State: uint8(e.state), Timer: remainingSec}.
//
// sessions, udp, and seq may be nil (e.g. in unit tests) — broadcasts are skipped
// gracefully in that case.
func (e *EmissionOrchestrator) Tick(
	now time.Time,
	sessions *network.SessionManager,
	udp *network.UDPListener,
	seq *atomic.Uint32,
) {
	e.mu.Lock()

	// If a forced emission was triggered, broadcast immediately.
	if e.forcePendingBroadcast {
		e.forcePendingBroadcast = false
		remSec := uint32(0)
		if e.nextTick.After(now) {
			remSec = uint32(e.nextTick.Sub(now).Seconds())
		}
		if remSec == 0 && e.warningDuration.Seconds() > 0 {
			remSec = uint32(e.warningDuration.Seconds())
		}
		e.mu.Unlock()
		e.broadcast(sessions, udp, seq, remSec)
		return
	}

	if now.Before(e.nextTick) {
		e.mu.Unlock()
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
	default:
		e.state = EmissionDormant
		nextDuration = e.dormantDuration
	}
	e.nextTick = now.Add(nextDuration)
	timerSec := uint32(nextDuration.Seconds())

	e.mu.Unlock()

	e.broadcast(sessions, udp, seq, timerSec)
}

// broadcast sends a WorldEventPayload to all connected sessions.
// Unexported method used internally and accessible within package game tests.
func (e *EmissionOrchestrator) broadcast(
	sessions *network.SessionManager,
	udp *network.UDPListener,
	seq *atomic.Uint32,
	timerSec uint32,
) {
	e.Broadcast(sessions, udp, seq, timerSec)
}

// Broadcast sends a WorldEventPayload to all connected sessions.
// Safe to call concurrently with other EmissionOrchestrator methods.
func (e *EmissionOrchestrator) Broadcast(
	sessions *network.SessionManager,
	udp *network.UDPListener,
	seq *atomic.Uint32,
	timerSec uint32,
) {
	if sessions == nil || seq == nil || (udp == nil && activeSink == nil) {
		return
	}

	e.mu.RLock()
	st := e.state
	e.mu.RUnlock()

	pkt := protocol.WorldEventPayload{
		EventType: emissionEventType,
		State:     uint8(st),
		Timer:     timerSec,
	}

	for _, sess := range sessions.GetAll() {
		if sess == nil {
			continue
		}

		sess.Lock()
		udpAddr := sess.UDPAddr
		sess.Unlock()

		if udpAddr == nil {
			continue
		}

		s := seq.Add(1)
		var buf bytes.Buffer
		if err := protocol.WritePacket(&buf, protocol.OpWorldEvent, s, protocol.FlagReliable, pkt); err != nil {
			continue
		}
		data := buf.Bytes()
		if activeSink != nil {
			activeSink.Record(data)
		}
		if udp != nil {
			_ = udp.Send(udpAddr, data)
		}
	}
}
