package protocol

import (
	"bufio"
	"io"
	"strings"

	drppb "github.com/cagojeiger/drp/proto/drp"
	"google.golang.org/protobuf/encoding/protodelim"
)

const (
	maxEnvelopeSize = 4 << 20   // 4 MiB
	pipeBufSize     = 64 * 1024 // 64 KiB
)

func WriteEnvelope(w io.Writer, env *drppb.Envelope) error {
	_, err := protodelim.MarshalTo(w, env)
	return err
}

func ReadEnvelope(r *bufio.Reader) (*drppb.Envelope, error) {
	env := &drppb.Envelope{}
	err := (protodelim.UnmarshalOptions{MaxSize: maxEnvelopeSize}).UnmarshalFrom(r, env)
	return env, err
}

func Pipe(dst io.WriteCloser, src io.Reader) error {
	buf := make([]byte, pipeBufSize)
	_, err := io.CopyBuffer(dst, src, buf)
	return err
}

func ExtractHost(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) < len("host:") || !strings.EqualFold(line[:len("host:")], "host:") {
			continue
		}

		host := strings.TrimSpace(line[len("host:"):])
		if host == "" {
			return ""
		}

		if strings.HasPrefix(host, "[") {
			end := strings.IndexByte(host, ']')
			if end <= 1 {
				return ""
			}
			return strings.ToLower(host[1:end])
		}

		if strings.Count(host, ":") == 1 {
			if h, _, ok := strings.Cut(host, ":"); ok {
				host = h
			}
		}

		return strings.ToLower(strings.TrimSpace(host))
	}

	return ""
}

func ExtractSNI(hello []byte) string {
	if len(hello) < 5 || hello[0] != 0x16 {
		return ""
	}

	recordLen := int(hello[3])<<8 | int(hello[4])
	if len(hello) < 5+recordLen {
		return ""
	}

	record := hello[5 : 5+recordLen]
	if len(record) < 4 || record[0] != 0x01 {
		return ""
	}

	hsLen := int(record[1])<<16 | int(record[2])<<8 | int(record[3])
	if len(record) < 4+hsLen {
		return ""
	}

	ch := record[4 : 4+hsLen]
	if len(ch) < 34 {
		return ""
	}

	i := 34
	if i+1 > len(ch) {
		return ""
	}
	i += 1 + int(ch[i])
	if i+2 > len(ch) {
		return ""
	}
	csLen := int(ch[i])<<8 | int(ch[i+1])
	i += 2 + csLen
	if i+1 > len(ch) {
		return ""
	}
	compLen := int(ch[i])
	i += 1 + compLen
	if i+2 > len(ch) {
		return ""
	}
	extLen := int(ch[i])<<8 | int(ch[i+1])
	i += 2
	if i+extLen > len(ch) {
		return ""
	}

	end := i + extLen
	for i+4 <= end {
		extType := int(ch[i])<<8 | int(ch[i+1])
		extDataLen := int(ch[i+2])<<8 | int(ch[i+3])
		i += 4
		if i+extDataLen > end {
			return ""
		}

		if extType == 0 {
			ext := ch[i : i+extDataLen]
			if len(ext) < 5 {
				return ""
			}
			nameListLen := int(ext[0])<<8 | int(ext[1])
			if 2+nameListLen > len(ext) {
				return ""
			}
			if ext[2] != 0 {
				return ""
			}
			nameLen := int(ext[3])<<8 | int(ext[4])
			if 5+nameLen > len(ext) {
				return ""
			}
			return strings.ToLower(string(ext[5 : 5+nameLen]))
		}

		i += extDataLen
	}

	return ""
}
