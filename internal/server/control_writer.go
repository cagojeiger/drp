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

	// Control channel plumbing.
	controlWriteBufferBytes = 64 * 1024
	controlReqChSize        = 2048
	controlSendChSize       = 1024
)

// sendLoop is the single writer goroutine for one control channel.
//
// Invariants:
//   - Every byte written to w passes through this goroutine — no other
//     goroutine may touch the encrypted writer.
//   - Refill signals from reqCh are merged into batched ReqWorkConn{Count: N}
//     messages; all other control messages from sendCh are written immediately
//     with surrounding flushes so response latency stays low.
//   - The loop terminates once both channels are drained AND closed.
//
// The closures below (flush, writeReq, writeReqBatch, flushDelay, batchLimit)
// are kept local because they all share the same bufio.Writer, flush timer,
// and pendingFlush bookkeeping — promoting them to methods would require
// threading state through every call.
func sendLoop(w io.Writer, reqCh <-chan struct{}, sendCh <-chan msg.Message, stats *ReqWorkConnStats) {
	bw := bufio.NewWriterSize(w, controlWriteBufferBytes)
	defer bw.Flush()

	flushTimer := time.NewTimer(time.Hour)
	stopTimer(flushTimer)

	pendingFlush := false

	flushDelay := func() time.Duration {
		q := len(reqCh)
		switch {
		case q >= 512:
			return reqWorkFlushFloor
		case q >= 128:
			return reqWorkFlushDefault
		default:
			return reqWorkFlushCeil
		}
	}

	batchLimit := func() int {
		q := len(reqCh)
		switch {
		case q >= 512:
			return reqWorkBatchMax
		case q >= 128:
			return reqWorkBatchMid
		default:
			return reqWorkBatchLow
		}
	}

	flush := func() error {
		if !pendingFlush {
			return nil
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		pendingFlush = false
		stopTimer(flushTimer)
		return nil
	}

	writeReq := func(count int) error {
		if count <= 0 {
			return nil
		}
		if stats != nil {
			c := int64(count)
			stats.sent.Add(c)
			stats.inflight.Add(-c)
		}
		if err := msg.WriteMsg(bw, &msg.ReqWorkConn{Count: count}); err != nil {
			return err
		}
		pendingFlush = true
		resetTimer(flushTimer, flushDelay())
		return nil
	}

	writeReqBatch := func() error {
		if reqCh == nil {
			return nil
		}
		limit := batchLimit()
		if limit <= 0 {
			limit = 1
		}
		batch := 0
		for batch < limit {
			select {
			case _, ok := <-reqCh:
				if !ok {
					reqCh = nil
					return nil
				}
				batch++
			default:
				if batch == 0 {
					return nil
				}
				return writeReq(batch)
			}
		}
		if err := writeReq(batch); err != nil {
			return err
		}
		if limit >= reqWorkBatchMid {
			return flush()
		}
		return nil
	}

	for reqCh != nil || sendCh != nil {
		// Prioritize refill signals so the work-conn pool stays warm.
		select {
		case _, ok := <-reqCh:
			if !ok {
				reqCh = nil
				continue
			}
			if err := writeReq(1); err != nil {
				return
			}
			if err := writeReqBatch(); err != nil {
				return
			}
			continue
		default:
		}

		select {
		case _, ok := <-reqCh:
			if !ok {
				reqCh = nil
				continue
			}
			if err := writeReq(1); err != nil {
				return
			}
			if err := writeReqBatch(); err != nil {
				return
			}
		case m, ok := <-sendCh:
			if !ok {
				sendCh = nil
				continue
			}
			// Control responses flush immediately on both sides to
			// minimize observed latency.
			if err := flush(); err != nil {
				return
			}
			if err := msg.WriteMsg(bw, m); err != nil {
				return
			}
			pendingFlush = true
			if err := flush(); err != nil {
				return
			}
		case <-flushTimer.C:
			if err := flush(); err != nil {
				return
			}
		}
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
