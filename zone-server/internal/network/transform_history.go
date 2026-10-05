package network

import "time"

// TransformRingCapacity is the number of transform samples kept per session.
// At the 30 Hz client transform rate this holds ~2.1 s, comfortably covering
// the 1.5 s lag-switch history window and the 1 s damage burst window.
const TransformRingCapacity = 64

// TransformSample is one accepted transform observation: server wall-clock
// time plus the validated position and gvid at that moment.
type TransformSample struct {
	At   time.Time
	X    float32
	Y    float32
	Z    float32
	Gvid uint16
}

// TransformRing is a fixed-capacity ring of recent accepted transforms. The
// zero value is ready to use and every method is allocation-free, so pushing
// on the 30 Hz transform path costs no garbage. It is not goroutine-safe;
// PlayerSession callers mutate it under the session lock.
type TransformRing struct {
	samples [TransformRingCapacity]TransformSample
	head    int
	count   int
}

// Push records one accepted sample, overwriting the oldest when full.
func (r *TransformRing) Push(s TransformSample) {
	r.samples[r.head] = s
	r.head = (r.head + 1) % TransformRingCapacity
	if r.count < TransformRingCapacity {
		r.count++
	}
}

// Len returns the number of stored samples.
func (r *TransformRing) Len() int {
	return r.count
}

// Latest returns the most recently pushed sample.
func (r *TransformRing) Latest() (TransformSample, bool) {
	if r.count == 0 {
		return TransformSample{}, false
	}
	idx := (r.head - 1 + TransformRingCapacity) % TransformRingCapacity
	return r.samples[idx], true
}

// OldestWithin returns the oldest sample whose timestamp is not before
// now-window, so callers can bound displacement over a rolling interval.
// It reports false when no sample falls inside the window.
func (r *TransformRing) OldestWithin(window time.Duration, now time.Time) (TransformSample, bool) {
	if r.count == 0 {
		return TransformSample{}, false
	}
	cutoff := now.Add(-window)
	// Walk newest-first to keep the scan cheap; remember the oldest in-window.
	start := (r.head - 1 + TransformRingCapacity) % TransformRingCapacity
	var oldest TransformSample
	found := false
	for i := 0; i < r.count; i++ {
		idx := (start - i + TransformRingCapacity) % TransformRingCapacity
		s := r.samples[idx]
		if s.At.Before(cutoff) {
			break
		}
		oldest = s
		found = true
	}
	return oldest, found
}
