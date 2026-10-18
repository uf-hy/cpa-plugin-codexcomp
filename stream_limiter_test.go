package main

import (
	"testing"
	"time"
)

func TestStreamExecutionLimiterBoundsAndReleases(t *testing.T) {
	var limiter streamExecutionLimiter

	if !limiter.tryAcquire(2) {
		t.Fatal("first execution slot should be available")
	}
	if !limiter.tryAcquire(2) {
		t.Fatal("second execution slot should be available")
	}
	if limiter.tryAcquire(2) {
		t.Fatal("third execution slot must be rejected")
	}
	if got := limiter.activeCount(); got != 2 {
		t.Fatalf("active count = %d, want 2", got)
	}

	limiter.release()
	if !limiter.tryAcquire(2) {
		t.Fatal("released execution slot should be reusable")
	}

	limiter.release()
	limiter.release()
	if got := limiter.activeCount(); got != 0 {
		t.Fatalf("active count = %d, want 0", got)
	}
}

func TestStreamExecutionLimiterHonorsLowerRuntimeLimit(t *testing.T) {
	var limiter streamExecutionLimiter

	if !limiter.tryAcquire(2) || !limiter.tryAcquire(2) {
		t.Fatal("expected two active execution slots")
	}
	if limiter.tryAcquire(1) {
		t.Fatal("lowered limit must reject new work while active count is above it")
	}

	limiter.release()
	if limiter.tryAcquire(1) {
		t.Fatal("new work must still be rejected while active count equals the limit")
	}

	limiter.release()
	if !limiter.tryAcquire(1) {
		t.Fatal("new work should be accepted once active count drops below the limit")
	}
	limiter.release()
}

func TestDefaultMaxConcurrentStreamsIsSafeAndPositive(t *testing.T) {
	cfg := defaultFoldConfig()
	if cfg.MaxConcurrentStreams != defaultMaxConcurrentStreams {
		t.Fatalf("MaxConcurrentStreams = %d, want %d", cfg.MaxConcurrentStreams, defaultMaxConcurrentStreams)
	}
	if cfg.MaxConcurrentStreams < 1 {
		t.Fatalf("MaxConcurrentStreams must be positive, got %d", cfg.MaxConcurrentStreams)
	}
}

func TestBlockedStreamStartsAreBounded(t *testing.T) {
	const (
		limit    = 4
		attempts = 64
	)

	var limiter streamExecutionLimiter
	unblock := make(chan struct{})
	started := make(chan struct{}, limit)
	finished := make(chan struct{}, limit)
	accepted := 0

	for i := 0; i < attempts; i++ {
		if !limiter.tryAcquire(limit) {
			continue
		}
		accepted++
		probeRoundStart(
			func() (hostModelStreamResponse, error) {
				started <- struct{}{}
				<-unblock
				return hostModelStreamResponse{StreamID: "upstream"}, nil
			},
			time.Millisecond,
			func(roundStartResult) {},
			func() {
				limiter.release()
				finished <- struct{}{}
			},
		)
	}

	if accepted != limit {
		t.Fatalf("accepted starts = %d, want %d", accepted, limit)
	}
	for i := 0; i < limit; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("only %d of %d accepted starts began", i, limit)
		}
	}
	if got := limiter.activeCount(); got != limit {
		t.Fatalf("active count while callbacks are blocked = %d, want %d", got, limit)
	}

	close(unblock)
	for i := 0; i < limit; i++ {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatalf("only %d of %d blocked starts released their slots", i, limit)
		}
	}
	if got := limiter.activeCount(); got != 0 {
		t.Fatalf("active count after callbacks completed = %d, want 0", got)
	}
}
