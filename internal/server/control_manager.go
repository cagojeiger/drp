package server

import (
	"context"
	"sync"

	"github.com/kangheeyong/drp/internal/msg"
)

// controlEntry holds the cancel function and send channels for a single
// control session. Every write to the control connection flows through
// sendCh → sendLoop (single-writer invariant).
type controlEntry struct {
	cancel context.CancelFunc
	reqCh  chan struct{}
	sendCh chan msg.Message
	done   <-chan struct{}
}

// Send enqueues a message to the control channel's write queue.
// Non-blocking: the message is dropped if the queue is full. Used for
// responses where latency matters more than delivery guarantees.
func (ce *controlEntry) Send(m msg.Message) {
	select {
	case ce.sendCh <- m:
	default:
	}
}

// SendReqWorkConn enqueues a ReqWorkConn refill signal with a delivery
// guarantee while the control session is alive. Returns false when the
// session has already been torn down.
//
// This is race-free without recover() because cleanupControlSession no
// longer closes reqCh — shutdown flows exclusively through ctx.Done()
// (ce.done here), and sending on an open buffered channel never panics.
func (ce *controlEntry) SendReqWorkConn() bool {
	select {
	case ce.reqCh <- struct{}{}:
		return true
	case <-ce.done:
		return false
	}
}

// controlManager tracks active control sessions indexed by runID so that a
// reconnect from the same frpc can cleanly displace the old session.
type controlManager struct {
	mu      sync.RWMutex
	entries map[string]*controlEntry
}

// Register inserts or replaces a control session. If a prior session with
// the same runID exists, its context is cancelled so its goroutines wind
// down before the new session starts serving.
func (cm *controlManager) Register(runID string, cancel context.CancelFunc, reqCh chan struct{}, sendCh chan msg.Message, done <-chan struct{}) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.entries == nil {
		cm.entries = make(map[string]*controlEntry)
	}
	if old, ok := cm.entries[runID]; ok {
		old.cancel()
	}
	cm.entries[runID] = &controlEntry{
		cancel: cancel,
		reqCh:  reqCh,
		sendCh: sendCh,
		done:   done,
	}
}

// Remove drops the entry for runID. Callers are responsible for cancelling
// the session beforehand; Remove only touches the map.
func (cm *controlManager) Remove(runID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.entries, runID)
}

// GetEntry returns the control session for runID, or (nil, false) if none.
// Uses a read lock so hot paths (ReqWorkConnFunc) scale across cores.
func (cm *controlManager) GetEntry(runID string) (*controlEntry, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	e, ok := cm.entries[runID]
	return e, ok
}
