package server

import "sync/atomic"

// ReqWorkConnStats tracks ReqWorkConn lifecycle counters for the control
// channel. Every counter is lock-free and safe for concurrent use.
//
//	requested → Handler requested a work-conn refill (one increment per call).
//	enqueued  → request accepted into the control writer's queue.
//	dropped   → request rejected because the control session is already gone.
//	sent      → request actually written onto the wire by sendLoop.
//	inflight  → enqueued − sent (pending work-conn demand, should drift to 0).
type ReqWorkConnStats struct {
	requested atomic.Int64
	enqueued  atomic.Int64
	dropped   atomic.Int64
	sent      atomic.Int64
	inflight  atomic.Int64
}

// ReqWorkConnSnapshot is a point-in-time, JSON-serializable view of
// ReqWorkConnStats.
type ReqWorkConnSnapshot struct {
	Requested int64 `json:"requested"`
	Enqueued  int64 `json:"enqueued"`
	Dropped   int64 `json:"dropped"`
	Sent      int64 `json:"sent"`
	Inflight  int64 `json:"inflight"`
}

// Snapshot returns a consistent read of every counter. Returns the zero value
// if s is nil so callers can omit nil checks.
func (s *ReqWorkConnStats) Snapshot() ReqWorkConnSnapshot {
	if s == nil {
		return ReqWorkConnSnapshot{}
	}
	return ReqWorkConnSnapshot{
		Requested: s.requested.Load(),
		Enqueued:  s.enqueued.Load(),
		Dropped:   s.dropped.Load(),
		Sent:      s.sent.Load(),
		Inflight:  s.inflight.Load(),
	}
}
