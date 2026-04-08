package server

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kangheeyong/drp/internal/auth"
	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

const defaultReadTimeout = 10 * time.Second

const (
	reqWorkBatchMax         = 128
	reqWorkBatchMid         = 64
	reqWorkBatchLow         = 16
	reqWorkFlushFloor       = 50 * time.Microsecond
	reqWorkFlushDefault     = 200 * time.Microsecond
	reqWorkFlushCeil        = 400 * time.Microsecond
	controlWriteBufferBytes = 64 * 1024
	controlReqChSize        = 2048
	controlSendChSize       = 1024
)

// Handler handles a single frpc stream (yamux or raw connection).
// It reads the first message and routes to Login or NewWorkConn.
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

type ReqWorkConnStats struct {
	requested atomic.Int64
	enqueued  atomic.Int64
	dropped   atomic.Int64
	sent      atomic.Int64
	inflight  atomic.Int64
}

type ReqWorkConnSnapshot struct {
	Requested int64 `json:"requested"`
	Enqueued  int64 `json:"enqueued"`
	Dropped   int64 `json:"dropped"`
	Sent      int64 `json:"sent"`
	Inflight  int64 `json:"inflight"`
}

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

// controlEntry holds the cancel function and send channel for a single control session.
// All writes to the control channel go through sendCh → sendLoop (single writer).
type controlEntry struct {
	cancel context.CancelFunc
	reqCh  chan struct{}
	sendCh chan msg.Message
	done   <-chan struct{}
}

// Send enqueues a message to the control channel's write queue.
// Non-blocking: drops the message if the queue is full.
func (ce *controlEntry) Send(m msg.Message) {
	select {
	case ce.sendCh <- m:
	default:
	}
}

// SendReqWorkConn enqueues ReqWorkConn with delivery guarantee while the control is alive.
func (ce *controlEntry) SendReqWorkConn() bool {
	defer func() {
		recover()
	}()
	select {
	case ce.reqCh <- struct{}{}:
		return true
	case <-ce.done:
		return false
	}
}

// controlManager tracks active controls by runID for reconnect handling.
type controlManager struct {
	mu      sync.RWMutex
	entries map[string]*controlEntry
}

func (cm *controlManager) Register(runID string, cancel context.CancelFunc, reqCh chan struct{}, sendCh chan msg.Message, done <-chan struct{}) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.entries == nil {
		cm.entries = make(map[string]*controlEntry)
	}
	if old, ok := cm.entries[runID]; ok {
		old.cancel()
	}
	cm.entries[runID] = &controlEntry{cancel: cancel, reqCh: reqCh, sendCh: sendCh, done: done}
}

func (cm *controlManager) Remove(runID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.entries, runID)
}

func (cm *controlManager) GetEntry(runID string) (*controlEntry, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	e, ok := cm.entries[runID]
	return e, ok
}

// ReqWorkConnFunc returns a function that sends ReqWorkConn on the control channel for the given runID.
// Non-blocking: the message is enqueued to sendCh and written by sendLoop.
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

// HandleConnection reads the first message and dispatches.
// After return, the caller should not use conn.
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

	// AES 래핑 시작 (서버: Writer 먼저 → IV 전송, 그 다음 Reader → IV 수신)
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

	// 제어 채널 write 큐 + 전용 sendLoop
	ctx, cancel := context.WithCancel(context.Background())
	reqCh := make(chan struct{}, controlReqChSize)
	sendCh := make(chan msg.Message, controlSendChSize)
	go sendLoop(encWriter, reqCh, sendCh, h.ReqStats)

	h.bootstrapReqWorkConn(login.PoolCount, reqCh)

	// 재연결 관리: 같은 RunID가 오면 old를 취소
	h.controls.Register(runID, cancel, reqCh, sendCh, ctx.Done())

	// 제어 루프: 암호화된 메시지 수신 → 처리
	registeredProxies := make(map[string]struct{})
	h.controlLoop(ctx, conn, encReader, sendCh, runID, registeredProxies)

	h.cleanupControlSession(cancel, reqCh, sendCh, runID, registeredProxies)
}

// sendLoop is the single writer goroutine for the control channel.
// It drains sendCh and writes messages sequentially to the encrypted writer.
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
		// Prioritize refill signals so work-conn pool stays warm.
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
			// control 응답은 지연을 줄이기 위해 즉시 flush한다.
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

func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	stopTimer(t)
	t.Reset(d)
}

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

func (h *Handler) cleanupControlSession(cancel context.CancelFunc, reqCh chan struct{}, sendCh chan msg.Message, runID string, registeredProxies map[string]struct{}) {
	// 연결 종료 시 정리
	cancel()
	close(reqCh)
	close(sendCh)
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

func (h *Handler) controlLoop(ctx context.Context, conn net.Conn, r io.Reader, sendCh chan msg.Message, runID string, registeredProxies map[string]struct{}) {
	defer conn.Close()

	// context 취소 시 conn을 닫아서 ReadMsg를 unblock
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	// heartbeat 타임아웃 감지
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

func (h *Handler) handleNewProxy(sendCh chan msg.Message, m *msg.NewProxy, runID string, registeredProxies map[string]struct{}) {
	if m.ProxyType != "http" {
		sendCh <- &msg.NewProxyResp{
			ProxyName: m.ProxyName,
			Error:     "only http proxy type is supported",
		}
		return
	}

	// 도메인 목록 결정
	domains := m.CustomDomains

	if h.Router != nil {
		// 각 도메인을 라우팅 테이블에 등록
		var registered []string
		for _, domain := range domains {
			cfg := &router.RouteConfig{
				Domain:            domain,
				Location:          "/",
				ProxyName:         m.ProxyName,
				RunID:             runID,
				UseEncryption:     m.UseEncryption,
				UseCompression:    m.UseCompression,
				HTTPUser:          m.HTTPUser,
				HTTPPwd:           m.HTTPPwd,
				HostHeaderRewrite: m.HostHeaderRewrite,
				Headers:           m.Headers,
				ResponseHeaders:   m.ResponseHeaders,
			}
			if len(m.Locations) > 0 {
				for _, loc := range m.Locations {
					locCfg := *cfg
					locCfg.Location = loc
					if err := h.Router.Add(&locCfg); err != nil {
						// 롤백: 이미 등록한 것들 제거
						for _, name := range registered {
							h.Router.Remove(name)
						}
						sendCh <- &msg.NewProxyResp{
							ProxyName: m.ProxyName,
							Error:     err.Error(),
						}
						return
					}
				}
			} else {
				if err := h.Router.Add(cfg); err != nil {
					// 롤백
					h.Router.Remove(m.ProxyName)
					sendCh <- &msg.NewProxyResp{
						ProxyName: m.ProxyName,
						Error:     err.Error(),
					}
					return
				}
			}
			registered = append(registered, m.ProxyName)
		}
		registeredProxies[m.ProxyName] = struct{}{}
	}

	sendCh <- &msg.NewProxyResp{
		ProxyName:  m.ProxyName,
		RemoteAddr: ":80",
	}
}
