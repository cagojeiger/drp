package server

import (
	"bufio"
	"io"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
)

// Control channel write-path tuning. These constants govern how the
// single-writer sendLoop batches ReqWorkConn refills versus how quickly it
// flushes the underlying encrypted writer.
//
// The batching is adaptive: when the refill backlog is deep we trade a bit
// of latency for higher throughput (bigger batches, shorter flush delays).
const (
	// Batch size ceilings chosen per backlog depth (reqCh length).
	reqWorkBatchMax = 128 // backlog ≥ 512
	reqWorkBatchMid = 64  // backlog ≥ 128
	reqWorkBatchLow = 16  // otherwise

	// Flush delays matched to the same backlog buckets. The ceiling is
	// intentionally tight (< 1ms) so interactive frpc clients do not see
	// perceptible queuing under idle conditions.
	reqWorkFlushFloor   = 50 * time.Microsecond  // backlog ≥ 512
	reqWorkFlushDefault = 200 * time.Microsecond // backlog ≥ 128
	reqWorkFlushCeil    = 400 * time.Microsecond // otherwise

	// Backlog thresholds that pick a batch/flush bucket.
	reqWorkBacklogHigh = 512
	reqWorkBacklogMid  = 128

	// Control channel plumbing.
	controlWriteBufferBytes = 64 * 1024
	controlReqChSize        = 2048
	controlSendChSize       = 1024
)

// controlWriter owns the single-writer goroutine that drains a control
// session's refill queue (reqCh) and response queue (sendCh) onto the
// encrypted writer.
//
// Invariants:
//   - Only run() may touch bw; no external goroutine writes directly.
//   - Refill signals are merged into batched ReqWorkConn{Count: N} messages.
//   - Response messages flush immediately on both sides so latency stays low.
//   - The loop exits when done fires (ctx cancellation) OR when the
//     underlying writer errors. Neither reqCh nor sendCh is ever closed —
//     shutdown is signalled exclusively via done, which lets external
//     senders race-freely fall through to their done-case.
//
// The type exists to replace a 150-line nest of closures that all shared
// the same bufio.Writer + flush timer + pending-flush flag. As fields on a
// struct, the shared state is explicit and each step is an ordinary method.
type controlWriter struct {
	bw           *bufio.Writer
	flushTimer   *time.Timer
	pendingFlush bool
	reqCh        <-chan struct{}
	sendCh       <-chan msg.Message
	done         <-chan struct{}
	stats        *ReqWorkConnStats
}

// sendLoop wires up a controlWriter for w and runs it to completion.
// Kept as a free function so callers (and tests) can use the same entry
// point as before. The loop terminates when done is closed or when any
// write on w returns an error.
func sendLoop(w io.Writer, reqCh <-chan struct{}, sendCh <-chan msg.Message, done <-chan struct{}, stats *ReqWorkConnStats) {
	cw := &controlWriter{
		bw:         bufio.NewWriterSize(w, controlWriteBufferBytes),
		flushTimer: time.NewTimer(time.Hour),
		reqCh:      reqCh,
		sendCh:     sendCh,
		done:       done,
		stats:      stats,
	}
	stopTimer(cw.flushTimer)
	cw.run()
}

// run is the main event loop. It prioritizes refill signals (so the
// work-conn pool stays warm) and falls back to a multi-way select that
// blocks on either channel, the flush timer, or the shutdown signal.
func (cw *controlWriter) run() {
	defer cw.bw.Flush()

	for {
		// Fast-exit check: if the session is already cancelled, bail
		// before doing any more work.
		select {
		case <-cw.done:
			return
		default:
		}

		// Fast path: drain refill signals before touching sendCh or the
		// flush timer. Keeps the pool responsive under burst load.
		select {
		case <-cw.reqCh:
			if err := cw.writeReq(1); err != nil {
				return
			}
			if err := cw.writeReqBatch(); err != nil {
				return
			}
			continue
		default:
		}

		select {
		case <-cw.done:
			return
		case <-cw.reqCh:
			if err := cw.writeReq(1); err != nil {
				return
			}
			if err := cw.writeReqBatch(); err != nil {
				return
			}
		case m := <-cw.sendCh:
			// Control responses flush on both sides to minimize the
			// observed round-trip latency.
			if err := cw.flush(); err != nil {
				return
			}
			if err := msg.WriteMsg(cw.bw, m); err != nil {
				return
			}
			cw.pendingFlush = true
			if err := cw.flush(); err != nil {
				return
			}
		case <-cw.flushTimer.C:
			if err := cw.flush(); err != nil {
				return
			}
		}
	}
}

// flush drains any buffered bytes and cancels the pending flush timer.
// No-op when nothing is pending.
func (cw *controlWriter) flush() error {
	if !cw.pendingFlush {
		return nil
	}
	if err := cw.bw.Flush(); err != nil {
		return err
	}
	cw.pendingFlush = false
	stopTimer(cw.flushTimer)
	return nil
}

// writeReq emits one ReqWorkConn{Count: count} message, updates counters,
// marks a flush as pending, and arms the flush timer for the adaptive delay.
func (cw *controlWriter) writeReq(count int) error {
	if count <= 0 {
		return nil
	}
	if cw.stats != nil {
		c := int64(count)
		cw.stats.sent.Add(c)
		cw.stats.inflight.Add(-c)
	}
	if err := msg.WriteMsg(cw.bw, &msg.ReqWorkConn{Count: count}); err != nil {
		return err
	}
	cw.pendingFlush = true
	resetTimer(cw.flushTimer, cw.flushDelay())
	return nil
}

// writeReqBatch non-blockingly pulls up to batchLimit() refill signals out
// of reqCh and emits them as a single ReqWorkConn{Count: N}. When the
// queue is deep enough to trigger the mid/high bucket it forces a flush so
// the bytes do not sit in the buffer.
func (cw *controlWriter) writeReqBatch() error {
	limit := cw.batchLimit()
	if limit <= 0 {
		limit = 1
	}
	batch := 0
	for batch < limit {
		select {
		case <-cw.reqCh:
			batch++
		default:
			if batch == 0 {
				return nil
			}
			return cw.writeReq(batch)
		}
	}
	if err := cw.writeReq(batch); err != nil {
		return err
	}
	if limit >= reqWorkBatchMid {
		return cw.flush()
	}
	return nil
}

// batchLimit picks the per-iteration batch ceiling based on the current
// refill backlog. Deeper backlogs trade a tiny latency penalty for higher
// throughput.
func (cw *controlWriter) batchLimit() int {
	q := len(cw.reqCh)
	switch {
	case q >= reqWorkBacklogHigh:
		return reqWorkBatchMax
	case q >= reqWorkBacklogMid:
		return reqWorkBatchMid
	default:
		return reqWorkBatchLow
	}
}

// flushDelay picks the flush-delay target for the current backlog bucket.
// Mirrors batchLimit() one-for-one so they always pick the same bucket.
func (cw *controlWriter) flushDelay() time.Duration {
	q := len(cw.reqCh)
	switch {
	case q >= reqWorkBacklogHigh:
		return reqWorkFlushFloor
	case q >= reqWorkBacklogMid:
		return reqWorkFlushDefault
	default:
		return reqWorkFlushCeil
	}
}

// stopTimer drains a time.Timer's channel after Stop so a subsequent Reset
// does not immediately re-fire on a stale value.
func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// resetTimer stops the timer (draining any pending tick) and arms it for d.
func resetTimer(t *time.Timer, d time.Duration) {
	stopTimer(t)
	t.Reset(d)
}
