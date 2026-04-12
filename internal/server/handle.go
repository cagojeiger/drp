package server

import (
	"context"
	"io"
	"log"
	"net"
	"time"

	"github.com/kangheeyong/drp/internal/auth"
	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

const defaultReadTimeout = 10 * time.Second

// Handler owns the server-side handling of frpc control and work-conn
// streams. A single Handler is shared by every frpc connection; per-session
// state lives in the controlManager and in the goroutines spawned by
// handleLogin.
type Handler struct {
	Token            string
	ReadTimeout      time.Duration
	HeartbeatTimeout time.Duration
	Router           *router.Router
	ReqStats         *ReqWorkConnStats
	OnWorkConn       func(conn net.Conn, m *msg.NewWorkConn)
	OnControlClose   func(runID string)
	controls         controlManager
}

// ReqWorkConnFunc returns a closure the pool can call to request another
// work-conn refill from the frpc side. The returned closure looks up the
// current control session for runID and enqueues a signal on its reqCh.
//
// Non-blocking: enqueue failures (session already torn down) are counted
// as dropped and the pool retries on its own cadence.
func (h *Handler) ReqWorkConnFunc(runID string) func() {
	return func() {
		e, ok := h.controls.GetEntry(runID)
		if !ok {
			return
		}
		if h.ReqStats != nil {
			h.ReqStats.requested.Add(1)
		}
		if e.SendReqWorkConn() {
			if h.ReqStats != nil {
				h.ReqStats.enqueued.Add(1)
				h.ReqStats.inflight.Add(1)
			}
			return
		}
		if h.ReqStats != nil {
			h.ReqStats.dropped.Add(1)
		}
	}
}

func (h *Handler) readTimeout() time.Duration {
	if h.ReadTimeout > 0 {
		return h.ReadTimeout
	}
	return defaultReadTimeout
}

// HandleConnection reads the first framed message from conn and dispatches
// based on its type: a Login opens a control session, a NewWorkConn feeds
// the work-conn pool. Any other first message closes the connection.
//
// After HandleConnection returns the caller must not reuse conn — either
// ownership has been handed off to a goroutine or conn is already closed.
func (h *Handler) HandleConnection(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(h.readTimeout()))
	rawMsg, err := msg.ReadMsg(conn)
	if err != nil {
		conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	switch m := rawMsg.(type) {
	case *msg.Login:
		h.handleLogin(conn, m)
	case *msg.NewWorkConn:
		if h.OnWorkConn != nil {
			h.OnWorkConn(conn, m)
		} else {
			conn.Close()
		}
	default:
		conn.Close()
	}
}

// handleLogin verifies credentials, performs the AES handshake, spawns the
// control-write goroutine, registers the session, and blocks on the control
// read loop until the connection closes.
func (h *Handler) handleLogin(conn net.Conn, login *msg.Login) {
	if !auth.VerifyAuth(h.Token, login.Timestamp, login.PrivilegeKey) {
		_ = msg.WriteMsg(conn, &msg.LoginResp{
			Version: "drps-0.1.0",
			Error:   "authorization failed",
		})
		conn.Close()
		return
	}

	runID := login.RunID
	if runID == "" {
		runID = generateRunID()
	}

	_ = msg.WriteMsg(conn, &msg.LoginResp{
		Version: "drps-0.1.0",
		RunID:   runID,
	})

	// AES wrapping handshake. Per-protocol ordering: writer first sends IV,
	// reader then receives IV.
	key := crypto.DeriveKey(h.Token)
	encWriter, err := crypto.NewCryptoWriter(conn, key)
	if err != nil {
		log.Printf("crypto writer: %v", err)
		conn.Close()
		return
	}
	encReader, err := crypto.NewCryptoReader(conn, key)
	if err != nil {
		log.Printf("crypto reader: %v", err)
		conn.Close()
		return
	}

	// Wire up the control write path and its single-writer goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	reqCh := make(chan struct{}, controlReqChSize)
	sendCh := make(chan msg.Message, controlSendChSize)
	done := ctx.Done()
	go sendLoop(encWriter, reqCh, sendCh, done, h.ReqStats)

	h.bootstrapReqWorkConn(login.PoolCount, reqCh)

	// A reconnect from the same runID cancels the old session before the
	// new one starts serving.
	h.controls.Register(runID, cancel, reqCh, sendCh, done)

	registeredProxies := make(map[string]struct{})
	h.controlLoop(ctx, conn, encReader, sendCh, runID, registeredProxies)

	h.cleanupControlSession(cancel, runID, registeredProxies)
}

// bootstrapReqWorkConn pre-fills the control write queue with N refill
// signals on behalf of frpc's initial pool size, which avoids a cold-start
// round-trip latency on the first proxied request.
func (h *Handler) bootstrapReqWorkConn(poolCount int, reqCh chan<- struct{}) {
	for range poolCount {
		if h.ReqStats != nil {
			h.ReqStats.requested.Add(1)
			h.ReqStats.enqueued.Add(1)
			h.ReqStats.inflight.Add(1)
		}
		reqCh <- struct{}{}
	}
}

// cleanupControlSession tears down one session's resources: cancel the
// context (which signals sendLoop to exit and unblocks any pending
// SendReqWorkConn callers via their done case), deregister from the
// manager, drop the router entries, and notify the owner via
// OnControlClose.
//
// Neither reqCh nor sendCh is closed. Shutdown flows exclusively through
// ctx.Done(), which removes the "send on closed channel" race that the
// previous close-based cleanup had to guard against with recover().
func (h *Handler) cleanupControlSession(cancel context.CancelFunc, runID string, registeredProxies map[string]struct{}) {
	cancel()
	h.controls.Remove(runID)
	if h.Router != nil {
		for name := range registeredProxies {
			h.Router.Remove(name)
		}
	}
	if h.OnControlClose != nil {
		h.OnControlClose(runID)
	}
}

// controlLoop reads framed messages from the encrypted reader and dispatches
// them. It owns conn for its lifetime and closes it on return. A separate
// goroutine watches ctx.Done() to unblock the reader if the session is
// cancelled externally (e.g. a reconnect from the same runID).
func (h *Handler) controlLoop(ctx context.Context, conn net.Conn, r io.Reader, sendCh chan msg.Message, runID string, registeredProxies map[string]struct{}) {
	defer conn.Close()

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	var heartbeatTimer *time.Timer
	if h.HeartbeatTimeout > 0 {
		heartbeatTimer = time.NewTimer(h.HeartbeatTimeout)
		defer heartbeatTimer.Stop()
		go func() {
			<-heartbeatTimer.C
			log.Printf("heartbeat timeout: runID=%s", runID)
			conn.Close()
		}()
	}

	for {
		rawMsg, err := msg.ReadMsg(r)
		if err != nil {
			return
		}

		switch m := rawMsg.(type) {
		case *msg.Ping:
			if heartbeatTimer != nil {
				heartbeatTimer.Reset(h.HeartbeatTimeout)
			}
			sendCh <- &msg.Pong{}
		case *msg.NewProxy:
			h.handleNewProxy(sendCh, m, runID, registeredProxies)
		case *msg.CloseProxy:
			if h.Router != nil {
				h.Router.Remove(m.ProxyName)
			}
			delete(registeredProxies, m.ProxyName)
		default:
			log.Printf("unexpected control message: %T", rawMsg)
		}
	}
}
