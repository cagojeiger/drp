// Package protocol implements the TLV+JSON wire protocol for drp.
//
// Frame format: [Type:1byte][Length:4bytes BE uint32][Body:JSON]
package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

const (
	MsgLogin         byte = 'L' // 0x4C
	MsgLoginResp     byte = 'l' // 0x6C
	MsgNewProxy      byte = 'P' // 0x50
	MsgNewProxyResp  byte = 'p' // 0x70
	MsgReqWorkConn   byte = 'R' // 0x52
	MsgNewWorkConn   byte = 'W' // 0x57
	MsgStartWorkConn byte = 'S' // 0x53
	MsgPing          byte = 'H' // 0x48
	MsgPong          byte = 'h' // 0x68
	MsgMeshHello     byte = 'M' // 0x4D
	MsgWhoHas        byte = 'F' // 0x46
	MsgIHave         byte = 'I' // 0x49
	MsgRelayOpen     byte = 'O' // 0x4F
)

type LoginBody struct {
	Alias string `json:"alias"`
}

type LoginRespBody struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type NewProxyBody struct {
	Alias    string `json:"alias"`
	Hostname string `json:"hostname"`
}

type NewProxyRespBody struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type ReqWorkConnBody struct{}

type NewWorkConnBody struct {
	Alias string `json:"alias"`
}

type StartWorkConnBody struct {
	Hostname string `json:"hostname"`
}

type MeshHelloBody struct {
	NodeID      string   `json:"node_id"`
	Peers       []string `json:"peers"`
	ControlPort int      `json:"control_port"`
}

type WhoHasBody struct {
	MsgID    string   `json:"msg_id"`
	Hostname string   `json:"hostname"`
	TTL      int      `json:"ttl"`
	Path     []string `json:"path"`
}

type IHaveBody struct {
	MsgID    string   `json:"msg_id"`
	Hostname string   `json:"hostname"`
	NodeID   string   `json:"node_id"`
	Path     []string `json:"path"`
}

type RelayOpenBody struct {
	RelayID  string   `json:"relay_id"`
	Hostname string   `json:"hostname"`
	NextHops []string `json:"next_hops"`
}

func ReadMsg(r io.Reader) (msgType byte, body []byte, err error) {
	var header [5]byte

	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}

	bodyLen := binary.BigEndian.Uint32(header[1:5])
	if bodyLen == 0 {
		return header[0], nil, nil
	}

	maxInt := int(^uint(0) >> 1)
	if bodyLen > uint32(maxInt) {
		return 0, nil, fmt.Errorf("protocol frame body too large: %d", bodyLen)
	}

	body = make([]byte, int(bodyLen))
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}

	return header[0], body, nil
}

func WriteMsg(w io.Writer, msgType byte, body interface{}) error {
	payload, err := marshalBody(body)
	if err != nil {
		return err
	}

	var header [5]byte
	header[0] = msgType
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))

	if err := writeAll(w, header[:]); err != nil {
		return err
	}

	if len(payload) == 0 {
		return nil
	}

	return writeAll(w, payload)
}

func Pipe(dst io.WriteCloser, src io.Reader) error {
	buf := make([]byte, 64*1024)
	var copyErr error

	for {
		n, err := src.Read(buf)
		if n > 0 {
			if writeErr := writeAll(dst, buf[:n]); writeErr != nil {
				copyErr = writeErr
				break
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				copyErr = nil
			} else {
				copyErr = err
			}
			break
		}
	}

	closeErr := dst.Close()
	if copyErr != nil {
		if closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
		return copyErr
	}

	if closeErr != nil {
		return closeErr
	}

	return nil
}

func ExtractHost(raw []byte) string {
	for _, line := range bytes.Split(raw, []byte("\r\n")) {
		idx := bytes.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}

		name := strings.TrimSpace(string(line[:idx]))
		if !strings.EqualFold(name, "Host") {
			continue
		}

		value := strings.TrimSpace(string(line[idx+1:]))
		if strings.HasPrefix(value, "[") {
			return value
		}

		host, _, found := strings.Cut(value, ":")
		if found {
			return host
		}

		return value
	}

	return ""
}

func GenerateID() string {
	b := make([]byte, 6)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "000000000000"
	}

	return hex.EncodeToString(b)
}

func marshalBody(body interface{}) ([]byte, error) {
	if body == nil || isEmptyStruct(body) {
		return nil, nil
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	return payload, nil
}

func isEmptyStruct(v interface{}) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return true
		}
		rv = rv.Elem()
	}

	rt := rv.Type()
	return rt.Kind() == reflect.Struct && rt.NumField() == 0
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}

		data = data[n:]
	}

	return nil
}
