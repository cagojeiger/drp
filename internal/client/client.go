package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/transport"
)

type Config struct {
	ServerAddr string
	Alias      string
	Hostname   string
	LocalAddr  string
	Verbose    bool
}

type Client struct {
	cfg   Config
	ready chan struct{}
}

func New(cfg Config) *Client {
	return &Client{
		cfg:   cfg,
		ready: make(chan struct{}),
	}
}

func (c *Client) Run(ctx context.Context) error {
	ctrlConn, err := transport.Dial(c.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("connect to server: %w", err)
	}
	defer func() { _ = ctrlConn.Close() }()
	log.Printf("[drpc] connected to %s", c.cfg.ServerAddr)

	if err := protocol.WriteMsg(ctrlConn, protocol.MsgLogin, &protocol.LoginBody{Alias: c.cfg.Alias}); err != nil {
		return fmt.Errorf("send login: %w", err)
	}

	msgType, body, err := protocol.ReadMsg(ctrlConn)
	if err != nil {
		return fmt.Errorf("read login resp: %w", err)
	}
	if msgType != protocol.MsgLoginResp {
		return fmt.Errorf("expected LoginResp, got 0x%02x", msgType)
	}
	var loginResp protocol.LoginRespBody
	if len(body) > 0 {
		if err := json.Unmarshal(body, &loginResp); err != nil {
			return fmt.Errorf("unmarshal login resp: %w", err)
		}
	}
	if !loginResp.OK {
		return fmt.Errorf("login failed: %s", loginResp.Message)
	}
	log.Printf("[drpc] login OK")

	if err := protocol.WriteMsg(ctrlConn, protocol.MsgNewProxy, &protocol.NewProxyBody{
		Alias:    c.cfg.Alias,
		Hostname: c.cfg.Hostname,
	}); err != nil {
		return fmt.Errorf("send new proxy: %w", err)
	}

	msgType, body, err = protocol.ReadMsg(ctrlConn)
	if err != nil {
		return fmt.Errorf("read proxy resp: %w", err)
	}
	if msgType != protocol.MsgNewProxyResp {
		return fmt.Errorf("expected NewProxyResp, got 0x%02x", msgType)
	}
	var proxyResp protocol.NewProxyRespBody
	if len(body) > 0 {
		if err := json.Unmarshal(body, &proxyResp); err != nil {
			return fmt.Errorf("unmarshal proxy resp: %w", err)
		}
	}
	if !proxyResp.OK {
		return fmt.Errorf("proxy registration failed: %s", proxyResp.Message)
	}
	log.Printf("[drpc] registered %s -> %s", c.cfg.Alias, c.cfg.Hostname)

	close(c.ready)

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.controlLoop(ctrlConn)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Ready() <-chan struct{} {
	return c.ready
}

func (c *Client) controlLoop(ctrlConn net.Conn) error {
	for {
		msgType, _, err := protocol.ReadMsg(ctrlConn)
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("control connection closed")
			}
			return err
		}

		if msgType == protocol.MsgReqWorkConn {
			go c.handleWorkConn()
		}
	}
}

func (c *Client) handleWorkConn() {
	workConn, err := transport.Dial(c.cfg.ServerAddr)
	if err != nil {
		log.Printf("[drpc] failed to open work conn: %v", err)
		return
	}

	if err := protocol.WriteMsg(workConn, protocol.MsgNewWorkConn, &protocol.NewWorkConnBody{Alias: c.cfg.Alias}); err != nil {
		_ = workConn.Close()
		return
	}

	msgType, body, err := protocol.ReadMsg(workConn)
	if err != nil {
		_ = workConn.Close()
		return
	}
	if msgType != protocol.MsgStartWorkConn {
		_ = workConn.Close()
		return
	}

	var swc protocol.StartWorkConnBody
	if len(body) > 0 {
		if err := json.Unmarshal(body, &swc); err != nil {
			_ = workConn.Close()
			return
		}
	}

	localConn, err := transport.Dial(c.cfg.LocalAddr)
	if err != nil {
		log.Printf("[drpc] failed to connect to local %s: %v", c.cfg.LocalAddr, err)
		_ = workConn.Close()
		return
	}

	go func() { _ = protocol.Pipe(workConn, localConn) }()
	_ = protocol.Pipe(localConn, workConn)
}
