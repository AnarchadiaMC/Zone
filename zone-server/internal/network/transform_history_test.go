package network

import (
	"testing"
	"time"
)

func TestTransformRingPushAndLatest(t *testing.T) {
	var r TransformRing
	if _, ok := r.Latest(); ok {
		t.Fatal("empty ring returned a latest sample")
	}
	base := time.Now()
	for i := 0; i < 3; i++ {
		r.Push(TransformSample{At: base.Add(time.Duration(i) * time.Second), X: float32(i)})
	}
	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3", r.Len())
	}
	latest, ok := r.Latest()
	if !ok || latest.X != 2 {
		t.Fatalf("Latest = %+v ok=%v, want X=2", latest, ok)
	}
}

func TestTransformRingBoundedOverwrite(t *testing.T) {
	var r TransformRing
	base := time.Now()
	for i := 0; i < TransformRingCapacity+10; i++ {
		r.Push(TransformSample{At: base.Add(time.Duration(i) * time.Millisecond), X: float32(i)})
	}
	if r.Len() != TransformRingCapacity {
		t.Fatalf("Len = %d, want capacity %d", r.Len(), TransformRingCapacity)
	}
	latest, _ := r.Latest()
	if latest.X != float32(TransformRingCapacity+9) {
		t.Fatalf("Latest X = %v, want %d", latest.X, TransformRingCapacity+9)
	}
}

func TestTransformRingOldestWithinWindow(t *testing.T) {
	var r TransformRing
	now := time.Now()
	r.Push(TransformSample{At: now.Add(-3 * time.Second), X: 1})
	r.Push(TransformSample{At: now.Add(-900 * time.Millisecond), X: 2})
	r.Push(TransformSample{At: now.Add(-100 * time.Millisecond), X: 3})

	oldest, ok := r.OldestWithin(time.Second, now)
	if !ok {
		t.Fatal("expected an in-window sample")
	}
	if oldest.X != 2 {
		t.Fatalf("oldest in-window X = %v, want 2", oldest.X)
	}

	// Window that only reaches the newest sample finds just that one.
	oldest, ok = r.OldestWithin(150*time.Millisecond, now)
	if !ok || oldest.X != 3 {
		t.Fatalf("tight window oldest = %+v ok=%v, want X=3", oldest, ok)
	}

	// A query time far after every sample reports none.
	none, ok := r.OldestWithin(time.Millisecond, now.Add(time.Hour))
	if ok {
		t.Fatalf("expected no sample in future window, got %+v", none)
	}
}

func BenchmarkTransformRingPush(b *testing.B) {
	var r TransformRing
	base := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Push(TransformSample{At: base, X: float32(i), Y: 1, Z: 2, Gvid: uint16(i)})
	}
}

func BenchmarkTransformRingOldestWithin(b *testing.B) {
	var r TransformRing
	now := time.Now()
	for i := 0; i < TransformRingCapacity; i++ {
		r.Push(TransformSample{At: now.Add(time.Duration(i-TransformRingCapacity) * time.Millisecond)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.OldestWithin(time.Second, now)
	}
}
