package feishu

import "fmt"

// header 和 frame 只覆盖飞书长连接用到的字段。
// 线格式是 protobuf：https://github.com/larksuite/oapi-sdk-go 里的 pbbp2。
type header struct {
	Key   string
	Value string
}

type frame struct {
	SeqID    uint64
	LogID    uint64
	Service  int32
	Method   int32
	Headers  []header
	Payload  []byte
	LogIDNew string
}

func (f frame) marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, f.SeqID)
	b = appendVarintField(b, 2, f.LogID)
	b = appendVarintField(b, 3, uint64(uint32(f.Service)))
	b = appendVarintField(b, 4, uint64(uint32(f.Method)))
	for _, h := range f.Headers {
		raw := h.marshal()
		b = appendBytesField(b, 5, raw)
	}
	if len(f.Payload) > 0 {
		b = appendBytesField(b, 8, f.Payload)
	}
	if f.LogIDNew != "" {
		b = appendBytesField(b, 9, []byte(f.LogIDNew))
	}
	return b
}

func (h header) marshal() []byte {
	var b []byte
	b = appendBytesField(b, 1, []byte(h.Key))
	b = appendBytesField(b, 2, []byte(h.Value))
	return b
}

func unmarshalFrame(b []byte) (frame, error) {
	var f frame
	for len(b) > 0 {
		num, typ, rest, err := takeKey(b)
		if err != nil {
			return frame{}, err
		}
		b = rest
		switch typ {
		case 0:
			v, rest, err := takeVarint(b)
			if err != nil {
				return frame{}, err
			}
			b = rest
			switch num {
			case 1:
				f.SeqID = v
			case 2:
				f.LogID = v
			case 3:
				f.Service = int32(v)
			case 4:
				f.Method = int32(v)
			}
		case 2:
			raw, rest, err := takeBytes(b)
			if err != nil {
				return frame{}, err
			}
			b = rest
			switch num {
			case 5:
				h, err := unmarshalHeader(raw)
				if err != nil {
					return frame{}, err
				}
				f.Headers = append(f.Headers, h)
			case 6, 7:
			case 8:
				f.Payload = append([]byte(nil), raw...)
			case 9:
				f.LogIDNew = string(raw)
			}
		default:
			return frame{}, fmt.Errorf("feishu frame: wire type %d", typ)
		}
	}
	return f, nil
}

func unmarshalHeader(b []byte) (header, error) {
	var h header
	for len(b) > 0 {
		num, typ, rest, err := takeKey(b)
		if err != nil {
			return header{}, err
		}
		b = rest
		if typ != 2 {
			return header{}, fmt.Errorf("feishu header: wire type %d", typ)
		}
		raw, rest, err := takeBytes(b)
		if err != nil {
			return header{}, err
		}
		b = rest
		switch num {
		case 1:
			h.Key = string(raw)
		case 2:
			h.Value = string(raw)
		}
	}
	return h, nil
}

func (f frame) header(key string) string {
	for _, h := range f.Headers {
		if h.Key == key {
			return h.Value
		}
	}
	return ""
}

func appendVarintField(b []byte, field int, v uint64) []byte {
	b = appendVarint(b, uint64(field<<3))
	return appendVarint(b, v)
}

func appendBytesField(b []byte, field int, p []byte) []byte {
	b = appendVarint(b, uint64(field<<3)|2)
	b = appendVarint(b, uint64(len(p)))
	return append(b, p...)
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func takeKey(b []byte) (num int, typ int, rest []byte, err error) {
	v, rest, err := takeVarint(b)
	if err != nil {
		return 0, 0, nil, err
	}
	return int(v >> 3), int(v & 7), rest, nil
}

func takeVarint(b []byte) (uint64, []byte, error) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		v |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return v, b[i+1:], nil
		}
	}
	return 0, nil, fmt.Errorf("feishu frame: bad varint")
}

func takeBytes(b []byte) ([]byte, []byte, error) {
	n, rest, err := takeVarint(b)
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(rest)) < n {
		return nil, nil, fmt.Errorf("feishu frame: short bytes")
	}
	return rest[:n], rest[n:], nil
}
