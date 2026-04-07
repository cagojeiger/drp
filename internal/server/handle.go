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

// controlManager tracks active controls by runID for reconnect handling.
type controlManager struct {
	mu       sync.Mutex
	sessions map[string]context.CancelFunc
	writers  map[string]io.Writer
}

func (cm *controlManager) Register(runID string, cancel context.CancelFunc, w io.Writer) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.sessions == nil {
		cm.sessions = make(map[string]context.CancelFunc)
		cm.writers = make(map[string]io.Writer)
	}
	if oldCancel, ok := cm.sessions[runID]; ok {
		oldCancel()
	}
	cm.sessions[runID] = cancel
	cm.writers[runID] = w
}

func (cm *controlManager) Remove(runID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.sessions, runID)
	delete(cm.writers, runID)
}

func (cm *controlManager) GetWriter(runID string) (io.Writer, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	w, ok := cm.writers[runID]
	return w, ok
}

// ReqWorkConnFunc returns a function that sends ReqWorkConn on the control channel for the given runID.
func (h *Handler) ReqWorkConnFunc(runID string) func() {
	return func() {
		w, ok := h.controls.GetWriter(runID)
		if !ok {
			return
		}
		_ = msg.WriteMsg(w, &msg.ReqWorkConn{})
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

	// ReqWorkConn × PoolCount
	for range login.PoolCount {
		if err := msg.WriteMsg(encWriter, &msg.ReqWorkConn{}); err != nil {
			conn.Close()
			return
		}
	}

	// 재연결 관리: 같은 RunID가 오면 old를 취소
	ctx, cancel := context.WithCancel(context.Background())
	h.controls.Register(runID, cancel, encWriter)

	// 제어 루프: 암호화된 메시지 수신 → 처리
	registeredProxies := []string{}
	h.controlLoop(ctx, conn, encReader, encWriter, runID, &registeredProxies)

	// 연결 종료 시 정리
	h.controls.Remove(runID)
	if h.Router != nil {
		for _, name := range registeredProxies {
			h.Router.Remove(name)
		}
	}
	if h.OnControlClose != nil {
		h.OnControlClose(runID)
	}
}

func (h *Handler) controlLoop(ctx context.Context, conn net.Conn, r io.Reader, w io.Writer, runID string, registeredProxies *[]string) {
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
			if err := msg.WriteMsg(w, &msg.Pong{}); err != nil {
				return
			}
		case *msg.NewProxy:
			h.handleNewProxy(w, m, runID, registeredProxies)
		case *msg.CloseProxy:
			if h.Router != nil {
				h.Router.Remove(m.ProxyName)
			}
			// registeredProxies에서도 제거
			for i, name := range *registeredProxies {
				if name == m.ProxyName {
					*registeredProxies = append((*registeredProxies)[:i], (*registeredProxies)[i+1:]...)
					break
				}
			}
		default:
			log.Printf("unexpected control message: %T", rawMsg)
		}
	}
}

func (h *Handler) handleNewProxy(w io.Writer, m *msg.NewProxy, runID string, registeredProxies *[]string) {
	if m.ProxyType != "http" {
		_ = msg.WriteMsg(w, &msg.NewProxyResp{
			ProxyName: m.ProxyName,
			Error:     "only http proxy type is supported",
		})
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
						_ = msg.WriteMsg(w, &msg.NewProxyResp{
							ProxyName: m.ProxyName,
							Error:     err.Error(),
						})
						return
					}
				}
			} else {
				if err := h.Router.Add(cfg); err != nil {
					// 롤백
					h.Router.Remove(m.ProxyName)
					_ = msg.WriteMsg(w, &msg.NewProxyResp{
						ProxyName: m.ProxyName,
						Error:     err.Error(),
					})
					return
				}
			}
			registered = append(registered, m.ProxyName)
		}
		*registeredProxies = append(*registeredProxies, m.ProxyName)
	}

	_ = msg.WriteMsg(w, &msg.NewProxyResp{
		ProxyName:  m.ProxyName,
		RemoteAddr: ":80",
	})
}
