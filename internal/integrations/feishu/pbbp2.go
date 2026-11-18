package feishu

// 飞书长连接使用的 pbbp2 帧（protobuf）。字段号与飞书官方 SDK（gogo 生成代码）一致，手写编解码以避免新增依赖：
//   Header { string key = 1; string value = 2; }
//   Frame  { uint64 SeqID = 1; uint64 LogID = 2; int32 service = 3; int32 method = 4; repeated Header headers = 5;
//            string payload_encoding = 6; string payload_type = 7; bytes payload = 8; string LogIDNew = 9; }

import (
	"errors"
	"io"
)

const (
	// FrameTypeControl / FrameTypeData 是 Frame.Method 的取值。
	FrameTypeControl int32 = 0
	FrameTypeData    int32 = 1

	// header 键与取值。
	HeaderType      = "type"
	HeaderMessageID = "message_id"
	HeaderSum       = "sum"
	HeaderSeq       = "seq"
	HeaderTraceID   = "trace_id"
	HeaderBizRT     = "biz_rt"

	TypeEvent = "event"
	TypeCard  = "card"
	TypePing  = "ping"
	TypePong  = "pong"

	// 握手失败时 HTTP 响应头里的状态。
	HeaderHandshakeStatus      = "Handshake-Status"
	HeaderHandshakeMsg         = "Handshake-Msg"
	HeaderHandshakeAuthErrCode = "Handshake-Autherrcode"

	// 握手状态与错误码。
	StatusAuthFailed     = 514
	StatusForbidden      = 403
	CodeExceedConnLimit  = 1000040350
	CodeSystemBusy       = 1
	CodeInternalError    = 1000040343
	maxFrameBytes        = 1 << 20
	maxFrameHeaders      = 64
	wireVarint           = 0
	wireFixed64          = 1
	wireBytes            = 2
	wireFixed32          = 5
	fieldSeqID           = 1
	fieldLogID           = 2
	fieldService         = 3
	fieldMethod          = 4
	fieldHeaders         = 5
	fieldPayloadEncoding = 6
	fieldPayloadType     = 7
	fieldPayload         = 8
	fieldLogIDNew        = 9
	fieldHeaderKey       = 1
	fieldHeaderValue     = 2
)

// ErrMalformedFrame 表示帧被截断、长度溢出或超过大小上限。
var ErrMalformedFrame = errors.New("feishu: malformed frame")

type Header struct {
	Key   string
	Value string
}

type Frame struct {
	SeqID           uint64
	LogID           uint64
	Service         int32
	Method          int32
	Headers         []Header
	PayloadEncoding string
	PayloadType     string
	Payload         []byte
	LogIDNew        string
}

// Header 返回第一个同名 header 的值。
func (f *Frame) Header(key string) string {
	for _, h := range f.Headers {
		if h.Key == key {
			return h.Value
		}
	}
	return ""
}

// SetHeader 设置（替换或追加）header。
func (f *Frame) SetHeader(key, value string) {
	for i := range f.Headers {
		if f.Headers[i].Key == key {
			f.Headers[i].Value = value
			return
		}
	}
	f.Headers = append(f.Headers, Header{Key: key, Value: value})
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendTag(b []byte, field, wire int) []byte { return appendVarint(b, uint64(field<<3|wire)) }

func appendString(b []byte, field int, s string) []byte {
	b = appendTag(b, field, wireBytes)
	b = appendVarint(b, uint64(len(s)))
	return append(b, s...)
}

func appendBytes(b []byte, field int, v []byte) []byte {
	b = appendTag(b, field, wireBytes)
	b = appendVarint(b, uint64(len(v)))
	return append(b, v...)
}

// Marshal 编码一帧；零值字段按 proto3 惯例省略，但 gogo（proto2 风格必填）生成的编码总是写出
// SeqID / LogID / service / method，这里同样总是写出这四个字段以保持字节一致。
func (f *Frame) Marshal() ([]byte, error) {
	b := make([]byte, 0, 64+len(f.Payload))
	b = appendTag(b, fieldSeqID, wireVarint)
	b = appendVarint(b, f.SeqID)
	b = appendTag(b, fieldLogID, wireVarint)
	b = appendVarint(b, f.LogID)
	b = appendTag(b, fieldService, wireVarint)
	b = appendVarint(b, uint64(int64(f.Service)))
	b = appendTag(b, fieldMethod, wireVarint)
	b = appendVarint(b, uint64(int64(f.Method)))
	for _, h := range f.Headers {
		var inner []byte
		inner = appendString(inner, fieldHeaderKey, h.Key)
		inner = appendString(inner, fieldHeaderValue, h.Value)
		b = appendBytes(b, fieldHeaders, inner)
	}
	if f.PayloadEncoding != "" {
		b = appendString(b, fieldPayloadEncoding, f.PayloadEncoding)
	}
	if f.PayloadType != "" {
		b = appendString(b, fieldPayloadType, f.PayloadType)
	}
	if len(f.Payload) > 0 {
		b = appendBytes(b, fieldPayload, f.Payload)
	}
	if f.LogIDNew != "" {
		b = appendString(b, fieldLogIDNew, f.LogIDNew)
	}
	if len(b) > maxFrameBytes {
		return nil, ErrMalformedFrame
	}
	return b, nil
}

func readVarint(b []byte) (uint64, int, error) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i] < 0x80 {
			return v, i + 1, nil
		}
	}
	if len(b) == 0 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	return 0, 0, ErrMalformedFrame
}

// readLengthDelimited 读出长度前缀的字节段，返回内容与总共消耗的字节数；长度溢出或截断返回错误。
func readLengthDelimited(b []byte) ([]byte, int, error) {
	length, n, err := readVarint(b)
	if err != nil {
		return nil, 0, ErrMalformedFrame
	}
	if length > uint64(len(b)-n) || length > maxFrameBytes {
		return nil, 0, ErrMalformedFrame
	}
	return b[n : n+int(length)], n + int(length), nil
}

// Unmarshal 解码一帧；未知字段按 wire type 跳过，截断、长度溢出、超过 1 MiB 返回 ErrMalformedFrame。
func (f *Frame) Unmarshal(b []byte) error {
	if len(b) > maxFrameBytes {
		return ErrMalformedFrame
	}
	*f = Frame{}
	for len(b) > 0 {
		tag, n, err := readVarint(b)
		if err != nil {
			return ErrMalformedFrame
		}
		b = b[n:]
		field, wire := int(tag>>3), int(tag&7)
		switch wire {
		case wireVarint:
			v, n, err := readVarint(b)
			if err != nil {
				return ErrMalformedFrame
			}
			b = b[n:]
			switch field {
			case fieldSeqID:
				f.SeqID = v
			case fieldLogID:
				f.LogID = v
			case fieldService:
				f.Service = int32(int64(v))
			case fieldMethod:
				f.Method = int32(int64(v))
			}
		case wireFixed64:
			if len(b) < 8 {
				return ErrMalformedFrame
			}
			b = b[8:]
		case wireFixed32:
			if len(b) < 4 {
				return ErrMalformedFrame
			}
			b = b[4:]
		case wireBytes:
			content, n, err := readLengthDelimited(b)
			if err != nil {
				return err
			}
			b = b[n:]
			switch field {
			case fieldHeaders:
				if len(f.Headers) >= maxFrameHeaders {
					return ErrMalformedFrame
				}
				h, err := unmarshalHeader(content)
				if err != nil {
					return err
				}
				f.Headers = append(f.Headers, h)
			case fieldPayloadEncoding:
				f.PayloadEncoding = string(content)
			case fieldPayloadType:
				f.PayloadType = string(content)
			case fieldPayload:
				f.Payload = append([]byte(nil), content...)
			case fieldLogIDNew:
				f.LogIDNew = string(content)
			}
		default:
			return ErrMalformedFrame
		}
	}
	return nil
}

func unmarshalHeader(b []byte) (Header, error) {
	var h Header
	for len(b) > 0 {
		tag, n, err := readVarint(b)
		if err != nil {
			return h, ErrMalformedFrame
		}
		b = b[n:]
		field, wire := int(tag>>3), int(tag&7)
		switch wire {
		case wireVarint:
			_, n, err := readVarint(b)
			if err != nil {
				return h, ErrMalformedFrame
			}
			b = b[n:]
		case wireFixed64:
			if len(b) < 8 {
				return h, ErrMalformedFrame
			}
			b = b[8:]
		case wireFixed32:
			if len(b) < 4 {
				return h, ErrMalformedFrame
			}
			b = b[4:]
		case wireBytes:
			content, n, err := readLengthDelimited(b)
			if err != nil {
				return h, err
			}
			b = b[n:]
			switch field {
			case fieldHeaderKey:
				h.Key = string(content)
			case fieldHeaderValue:
				h.Value = string(content)
			}
		default:
			return h, ErrMalformedFrame
		}
	}
	return h, nil
}
