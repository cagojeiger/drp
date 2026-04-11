package capture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kangheeyong/drp/test/compat/schema"
)

// DoHTTP executes req against endpoint (host:port) and returns a CapturedResponse.
// Used for http (non-streaming) scenarios.
func DoHTTP(ctx context.Context, endpoint string, req schema.RequestSpec) *CapturedResponse {
	url := "http://" + endpoint + req.Path
	var body io.Reader
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, body)
	if err != nil {
		return &CapturedResponse{Err: fmt.Errorf("build request: %w", err)}
	}
	httpReq.Host = req.Host
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if req.BasicAuth != nil {
		httpReq.SetBasicAuth(req.BasicAuth.User, req.BasicAuth.Pass)
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return &CapturedResponse{Err: fmt.Errorf("do request: %w", err)}
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &CapturedResponse{Err: fmt.Errorf("read body: %w", err)}
	}

	chunked := false
	for _, te := range resp.TransferEncoding {
		if te == "chunked" {
			chunked = true
			break
		}
	}

	return &CapturedResponse{
		Status:        resp.StatusCode,
		Headers:       resp.Header,
		Body:          bodyBytes,
		ChunkedOnWire: chunked,
	}
}

// DoWebSocket performs a raw HTTP/1.1 WebSocket upgrade against endpoint,
// sends the given text frames one at a time, reads one frame after each send,
// and returns the captured frame list.
//
// This is intentionally minimal: it assumes echo-style behavior (one request
// frame → one response frame). The Sec-WebSocket-Key is fixed so the upgrade
// response is deterministic across runs.
func DoWebSocket(ctx context.Context, endpoint, hostHeader, path string, sendFrames []string) *CapturedResponse {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(15 * time.Second)
	}

	conn, err := net.DialTimeout("tcp", endpoint, 5*time.Second)
	if err != nil {
		return &CapturedResponse{Err: fmt.Errorf("dial ws: %w", err)}
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	if path == "" {
		path = "/ws"
	}
	upgrade := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + hostHeader + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"\r\n"
	if _, err := conn.Write([]byte(upgrade)); err != nil {
		return &CapturedResponse{Err: fmt.Errorf("write upgrade: %w", err)}
	}

	// Read upgrade response headers (until \r\n\r\n).
	br := newBufReader(conn)
	headerBytes, err := br.readUntilDoubleCRLF()
	if err != nil {
		return &CapturedResponse{Err: fmt.Errorf("read upgrade headers: %w", err)}
	}
	status := parseStatusLine(headerBytes)
	if status != 101 {
		return &CapturedResponse{
			Err:    fmt.Errorf("upgrade status=%d, want 101 (headers: %q)", status, string(headerBytes)),
			Status: status,
		}
	}

	var frames []WSFrame
	for _, payload := range sendFrames {
		// Client → server frames MUST be masked per RFC 6455.
		out := buildClientTextFrame([]byte(payload))
		if _, err := conn.Write(out); err != nil {
			return &CapturedResponse{Err: fmt.Errorf("write frame: %w", err), Status: 101, WSFrames: frames}
		}
		frame, err := readServerFrame(br)
		if err != nil {
			return &CapturedResponse{Err: fmt.Errorf("read frame: %w", err), Status: 101, WSFrames: frames}
		}
		frames = append(frames, frame)
	}

	return &CapturedResponse{
		Status:   101,
		WSFrames: frames,
	}
}

// --- minimal WS frame helpers --------------------------------------------

func buildClientTextFrame(payload []byte) []byte {
	// opcode=0x1 (text), FIN=1, masked.
	var buf bytes.Buffer
	buf.WriteByte(0x81)
	plen := len(payload)
	mask := []byte{0x12, 0x34, 0x56, 0x78}
	switch {
	case plen < 126:
		buf.WriteByte(byte(plen) | 0x80)
	case plen <= 0xffff:
		buf.WriteByte(126 | 0x80)
		buf.WriteByte(byte(plen >> 8))
		buf.WriteByte(byte(plen))
	default:
		buf.WriteByte(127 | 0x80)
		for i := 7; i >= 0; i-- {
			buf.WriteByte(byte(plen >> (8 * i)))
		}
	}
	buf.Write(mask)
	masked := make([]byte, plen)
	for i := 0; i < plen; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	buf.Write(masked)
	return buf.Bytes()
}

func readServerFrame(br *bufReader) (WSFrame, error) {
	hdr, err := br.readN(2)
	if err != nil {
		return WSFrame{}, err
	}
	opcode := hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	plen := int(hdr[1] & 0x7f)
	switch plen {
	case 126:
		ext, err := br.readN(2)
		if err != nil {
			return WSFrame{}, err
		}
		plen = int(ext[0])<<8 | int(ext[1])
	case 127:
		ext, err := br.readN(8)
		if err != nil {
			return WSFrame{}, err
		}
		plen = 0
		for i := 0; i < 8; i++ {
			plen = plen<<8 | int(ext[i])
		}
	}
	var mask []byte
	if masked {
		mask, err = br.readN(4)
		if err != nil {
			return WSFrame{}, err
		}
	}
	payload, err := br.readN(plen)
	if err != nil {
		return WSFrame{}, err
	}
	if masked {
		for i := 0; i < plen; i++ {
			payload[i] ^= mask[i%4]
		}
	}
	return WSFrame{Opcode: opcode, Payload: payload}, nil
}

// --- tiny bufio replacement -----------------------------------------------

type bufReader struct {
	conn net.Conn
	buf  []byte
}

func newBufReader(c net.Conn) *bufReader { return &bufReader{conn: c} }

func (b *bufReader) readN(n int) ([]byte, error) {
	for len(b.buf) < n {
		tmp := make([]byte, 4096)
		m, err := b.conn.Read(tmp)
		if m > 0 {
			b.buf = append(b.buf, tmp[:m]...)
		}
		if err != nil && len(b.buf) < n {
			return nil, err
		}
	}
	out := b.buf[:n]
	b.buf = b.buf[n:]
	return out, nil
}

func (b *bufReader) readUntilDoubleCRLF() ([]byte, error) {
	for {
		if idx := bytes.Index(b.buf, []byte("\r\n\r\n")); idx >= 0 {
			out := b.buf[:idx+4]
			b.buf = b.buf[idx+4:]
			return out, nil
		}
		tmp := make([]byte, 4096)
		m, err := b.conn.Read(tmp)
		if m > 0 {
			b.buf = append(b.buf, tmp[:m]...)
		}
		if err != nil {
			return nil, err
		}
	}
}

func parseStatusLine(headers []byte) int {
	line := headers
	if idx := bytes.Index(line, []byte("\r\n")); idx >= 0 {
		line = line[:idx]
	}
	parts := strings.SplitN(string(line), " ", 3)
	if len(parts) < 2 {
		return 0
	}
	var n int
	_, err := fmt.Sscanf(parts[1], "%d", &n)
	if err != nil {
		return 0
	}
	return n
}
