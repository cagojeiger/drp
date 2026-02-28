package client

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/transport"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

type Config struct {
	ServerAddr  string           // drps control address (host:port)
	Alias       string           // service alias (e.g. "myapp")
	Hostname    string           // public hostname (e.g. "myapp.example.com")
	ProxyType   string           // "http" or "https", default "http"
	LocalAddr   string           // local service address (host:port)
	APIKey      string           // API key for authentication
	Version     string           // client version string
	LocalDialer transport.Dialer // optional, defaults to TCPDialer
}

type Client struct {
	cfg         Config
	dialer      transport.Dialer
	localDialer transport.Dialer
	ready       chan struct{}
}

func New(cfg Config, dialer transport.Dialer) *Client {
	if cfg.ProxyType == "" {
		cfg.ProxyType = "http"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	localDialer := cfg.LocalDialer
	if localDialer == nil {
		localDialer = transport.TCPDialer{}
	}
	return &Client{cfg: cfg, dialer: dialer, localDialer: localDialer, ready: make(chan struct{})}
}

func (c *Client) Run(ctx context.Context) error {
	conn, err := c.dialer.Dial(c.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{
			ApiKey:  c.cfg.APIKey,
			Version: c.cfg.Version,
		}},
	}); err != nil {
		return fmt.Errorf("send login: %w", err)
	}

	r := bufio.NewReader(conn)
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		return fmt.Errorf("read login response: %w", err)
	}
	loginResp := env.GetLoginResp()
	if loginResp == nil || !loginResp.Ok {
		errMsg := "invalid login response"
		if loginResp != nil && loginResp.Error != "" {
			errMsg = loginResp.Error
		}
		return fmt.Errorf("login failed: %s", errMsg)
	}
	if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
			Alias:    c.cfg.Alias,
			Hostname: c.cfg.Hostname,
			Type:     c.cfg.ProxyType,
		}},
	}); err != nil {
		return fmt.Errorf("send new proxy: %w", err)
	}

	env, err = protocol.ReadEnvelope(r)
	if err != nil {
		return fmt.Errorf("read new proxy response: %w", err)
	}
	proxyResp := env.GetNewProxyResp()
	if proxyResp == nil || !proxyResp.Ok {
		errMsg := "invalid new proxy response"
		if proxyResp != nil && proxyResp.Error != "" {
			errMsg = proxyResp.Error
		}
		return fmt.Errorf("new proxy failed: %s", errMsg)
	}
	close(c.ready)
	return c.controlLoop(ctx, conn, r)
}

func (c *Client) Ready() <-chan struct{} {
	return c.ready
}

func (c *Client) controlLoop(ctx context.Context, conn net.Conn, r *bufio.Reader) error {
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
					Payload: &drppb.Envelope_Ping{Ping: &drppb.Ping{}},
				}); err != nil {
					log.Printf("heartbeat write failed: %v", err)
					_ = conn.Close()
					return
				}
			}
		}
	}()

	for {
		env, err := protocol.ReadEnvelope(r)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		switch payload := env.Payload.(type) {
		case *drppb.Envelope_ReqWorkConn:
			go c.handleWorkConn(payload.ReqWorkConn.GetProxyAlias())
		case *drppb.Envelope_Pong:
		default:
			log.Printf("unexpected control message type: %T", payload)
		}
	}
}

func (c *Client) handleWorkConn(proxyAlias string) {
	workConn, err := c.dialer.Dial(c.cfg.ServerAddr)
	if err != nil {
		log.Printf("work conn dial failed: %v", err)
		return
	}
	defer func() { _ = workConn.Close() }()
	if err := protocol.WriteEnvelope(workConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewWorkConn{NewWorkConn: &drppb.NewWorkConn{
			ProxyAlias: proxyAlias,
		}},
	}); err != nil {
		log.Printf("send new_work_conn failed: %v", err)
		return
	}

	r := bufio.NewReader(workConn)
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		log.Printf("read start_work_conn failed: %v", err)
		return
	}
	if env.GetStartWorkConn() == nil {
		log.Printf("unexpected work conn response: %T", env.Payload)
		return
	}
	localConn, err := c.localDialer.Dial(c.cfg.LocalAddr)
	if err != nil {
		log.Printf("dial local service failed: %v", err)
		return
	}
	defer func() { _ = localConn.Close() }()

	go func() {
		if err := protocol.Pipe(workConn, localConn); err != nil {
			log.Printf("pipe local->work failed: %v", err)
		}
	}()
	if err := protocol.Pipe(localConn, r); err != nil {
		log.Printf("pipe work->local failed: %v", err)
	}
}
