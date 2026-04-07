package server

import (
	"context"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/kangheeyong/drp/internal/auth"
	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

const defaultReadTimeout = 10 * time.Second

// Handler handles a single frpc stream (yamux or raw connection).
// It reads the first message and routes to Login or NewWorkConn.
type Handler struct {
	Token            string
	ReadTimeout      time.Duration
	HeartbeatTimeout time.Duration
	Router           *router.Router
	OnWorkConn       func(conn net.Conn, m *msg.NewWorkConn)
	OnControlClose   func(runID string)
	controls         controlManager
}

// controlEntry holds the cancel function and send channel for a single control session.
// All writes to the control channel go through sendCh → sendLoop (single writer).
type controlEntry struct {
	cancel context.CancelFunc
	sendCh chan msg.Message
}

// Send enqueues a message to the control channel's write queue.
// Non-blocking: drops the message if the queue is full.
func (ce *controlEntry) Send(m msg.Message) {
	select {
	case ce.sendCh <- m:
	default:
	}
}

// controlManager tracks active controls by runID for reconnect handling.
type controlManager struct {
	mu      sync.RWMutex
	entries map[string]*controlEntry
}

func (cm *controlManager) Register(runID string, cancel context.CancelFunc, sendCh chan msg.Message) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.entries == nil {
		cm.entries = make(map[string]*controlEntry)
	}
	if old, ok := cm.entries[runID]; ok {
		old.cancel()
	}
	cm.entries[runID] = &controlEntry{cancel: cancel, sendCh: sendCh}
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
		e.Send(&msg.ReqWorkConn{})
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
	sendCh := make(chan msg.Message, 100)
	go sendLoop(encWriter, sendCh)

	// ReqWorkConn × PoolCount (sendLoop 시작 후)
	for range login.PoolCount {
		sendCh <- &msg.ReqWorkConn{}
	}

	// 재연결 관리: 같은 RunID가 오면 old를 취소
	ctx, cancel := context.WithCancel(context.Background())
	h.controls.Register(runID, cancel, sendCh)

	// 제어 루프: 암호화된 메시지 수신 → 처리
	registeredProxies := make(map[string]struct{})
	h.controlLoop(ctx, conn, encReader, sendCh, runID, registeredProxies)

	// 연결 종료 시 정리
	close(sendCh) // sendLoop 종료
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

// sendLoop is the single writer goroutine for the control channel.
// It drains sendCh and writes messages sequentially to the encrypted writer.
func sendLoop(w io.Writer, sendCh <-chan msg.Message) {
	for m := range sendCh {
		if err := msg.WriteMsg(w, m); err != nil {
			return
		}
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
