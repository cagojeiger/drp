package msg

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const MaxBodySize = 10240

// Message is the interface all frp protocol messages implement.
type Message interface {
	message()
}

// frp 메시지 타입 바이트 매핑
var (
	typeTo = map[byte]func() Message{
		'o': func() Message { return &Login{} },
		'1': func() Message { return &LoginResp{} },
		'p': func() Message { return &NewProxy{} },
		'2': func() Message { return &NewProxyResp{} },
		'c': func() Message { return &CloseProxy{} },
		'r': func() Message { return &ReqWorkConn{} },
		'w': func() Message { return &NewWorkConn{} },
		's': func() Message { return &StartWorkConn{} },
		'h': func() Message { return &Ping{} },
		'4': func() Message { return &Pong{} },
	}

	msgToType = map[string]byte{}
)

func init() {
	for b, fn := range typeTo {
		msg := fn()
		msgToType[fmt.Sprintf("%T", msg)] = b
	}
}

// TypeOf returns the wire type byte for a message.
func TypeOf(m Message) (byte, bool) {
	b, ok := msgToType[fmt.Sprintf("%T", m)]
	return b, ok
}

// WriteMsg writes a message in frp wire format: [1B type][8B length BE][JSON]
func WriteMsg(w io.Writer, m Message) error {
	typeByte, ok := TypeOf(m)
	if !ok {
		return fmt.Errorf("unknown message type: %T", m)
	}

	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if _, err := w.Write([]byte{typeByte}); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, int64(len(body))); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// ReadMsg reads a message from frp wire format.
func ReadMsg(r io.Reader) (Message, error) {
	var typeBuf [1]byte
	if _, err := io.ReadFull(r, typeBuf[:]); err != nil {
		return nil, err
	}

	var length int64
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if length > MaxBodySize {
		return nil, fmt.Errorf("message body too large: %d > %d", length, MaxBodySize)
	}

	fn, ok := typeTo[typeBuf[0]]
	if !ok {
		return nil, fmt.Errorf("unknown message type byte: 0x%02x", typeBuf[0])
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}

	msg := fn()
	if err := json.Unmarshal(body, msg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return msg, nil
}

// --- 메시지 구조체 (frp v0.68.0 호환, 원본 필드 완전 일치) ---

type Login struct {
	Version      string            `json:"version,omitempty"`
	Hostname     string            `json:"hostname,omitempty"`
	Os           string            `json:"os,omitempty"`
	Arch         string            `json:"arch,omitempty"`
	User         string            `json:"user,omitempty"`
	PrivilegeKey string            `json:"privilege_key,omitempty"`
	Timestamp    int64             `json:"timestamp,omitempty"`
	RunID        string            `json:"run_id,omitempty"`
	ClientID     string            `json:"client_id,omitempty"`
	Metas        map[string]string `json:"metas,omitempty"`
	PoolCount    int               `json:"pool_count,omitempty"`
}

type LoginResp struct {
	Version string `json:"version,omitempty"`
	RunID   string `json:"run_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

type NewProxy struct {
	ProxyName         string            `json:"proxy_name,omitempty"`
	ProxyType         string            `json:"proxy_type,omitempty"`
	UseEncryption     bool              `json:"use_encryption,omitempty"`
	UseCompression    bool              `json:"use_compression,omitempty"`
	BandwidthLimit    string            `json:"bandwidth_limit,omitempty"`
	BandwidthLimitMode string           `json:"bandwidth_limit_mode,omitempty"`
	Group             string            `json:"group,omitempty"`
	GroupKey          string            `json:"group_key,omitempty"`
	Metas             map[string]string `json:"metas,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	CustomDomains     []string          `json:"custom_domains,omitempty"`
	SubDomain         string            `json:"subdomain,omitempty"`
	Locations         []string          `json:"locations,omitempty"`
	HTTPUser          string            `json:"http_user,omitempty"`
	HTTPPwd           string            `json:"http_pwd,omitempty"`
	HostHeaderRewrite string            `json:"host_header_rewrite,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	ResponseHeaders   map[string]string `json:"response_headers,omitempty"`
	RouteByHTTPUser   string            `json:"route_by_http_user,omitempty"`
}

type NewProxyResp struct {
	ProxyName  string `json:"proxy_name,omitempty"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	Error      string `json:"error,omitempty"`
}

type CloseProxy struct {
	ProxyName string `json:"proxy_name,omitempty"`
}

type ReqWorkConn struct{}

type NewWorkConn struct {
	RunID        string `json:"run_id,omitempty"`
	PrivilegeKey string `json:"privilege_key,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
}

type StartWorkConn struct {
	ProxyName string `json:"proxy_name,omitempty"`
	SrcAddr   string `json:"src_addr,omitempty"`
	DstAddr   string `json:"dst_addr,omitempty"`
	SrcPort   uint16 `json:"src_port,omitempty"`
	DstPort   uint16 `json:"dst_port,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Ping struct {
	PrivilegeKey string `json:"privilege_key,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
}

type Pong struct {
	Error string `json:"error,omitempty"`
}

// Message interface 구현
func (*Login) message()         {}
func (*LoginResp) message()     {}
func (*NewProxy) message()      {}
func (*NewProxyResp) message()  {}
func (*CloseProxy) message()    {}
func (*ReqWorkConn) message()   {}
func (*NewWorkConn) message()   {}
func (*StartWorkConn) message() {}
func (*Ping) message()          {}
func (*Pong) message()          {}
