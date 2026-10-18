package main

import "sync/atomic"

// streamExecutionLimiter bounds calls into the synchronous host streaming API.
//
// A host callback may block in cgo indefinitely. A timeout in this plugin cannot
// cancel that callback safely, so a timed-out slot must not be reused while the
// callback is still running. Holding the slot for the complete fold lifecycle
// prevents blocked callbacks, locked OS threads, and their goroutines from
// accumulating without bound.
type streamExecutionLimiter struct {
	active atomic.Int64
}

func (l *streamExecutionLimiter) tryAcquire(limit int) bool {
	if limit < 1 {
		return false
	}
	for {
		active := l.active.Load()
		if active >= int64(limit) {
			return false
		}
		if l.active.CompareAndSwap(active, active+1) {
			return true
		}
	}
}

func (l *streamExecutionLimiter) release() {
	active := l.active.Add(-1)
	if active < 0 {
		l.active.Add(1)
		panic("stream execution limiter released without an active slot")
	}
}

func (l *streamExecutionLimiter) activeCount() int {
	return int(l.active.Load())
}

var activeStreamExecutions streamExecutionLimiter
