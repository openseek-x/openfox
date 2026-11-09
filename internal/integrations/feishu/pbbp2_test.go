package feishu

import (
	"bytes"
	"errors"
	"testing"
)

// 按 protobuf 编码规则手工构造的字节：SeqID=1, LogID=2, service=3, method=1, header{type=ping}, payload="ab"。
func TestFrameMarshalMatchesProtobufEncoding(t *testing.T) {
	frame := Frame{SeqID: 1, LogID: 2, Service: 3, Method: 1, Headers: []Header{{Key: "type", Value: "ping"}}, Payload: []byte("ab")}
	want := []byte{
		0x08, 0x01, // 1: SeqID
		0x10, 0x02, // 2: LogID
		0x18, 0x03, // 3: service
		0x20, 0x01, // 4: method
		0x2a, 0x0c, // 5: header，长度 12
		0x0a, 0x04, 't', 'y', 'p', 'e', 0x12, 0x04, 'p', 'i', 'n', 'g',
		0x42, 0x02, 'a', 'b', // 8: payload
	}
	got, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal = % x, want % x", got, want)
	}
}

func TestFrameNegativeInt32UsesSixtyFourBitVarint(t *testing.T) {
	frame := Frame{Service: -1}
	got, _ := frame.Marshal()
	// service = -1 编码为 10 字节 varint（64 位补码）。
	want := []byte{0x08, 0x00, 0x10, 0x00, 0x18, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01, 0x20, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal = % x, want % x", got, want)
	}
	var back Frame
	if err := back.Unmarshal(got); err != nil || back.Service != -1 {
		t.Fatalf("Unmarshal = %+v, %v", back, err)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	frame := Frame{SeqID: 99, LogID: 1 << 40, Service: 7, Method: 1, PayloadEncoding: "json", PayloadType: "event", LogIDNew: "x",
		Headers: []Header{{Key: "type", Value: "event"}, {Key: "message_id", Value: "m1"}}, Payload: bytes.Repeat([]byte{1}, 300)}
	data, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back Frame
	if err := back.Unmarshal(data); err != nil {
		t.Fatal(err)
	}
	if back.SeqID != 99 || back.LogID != 1<<40 || back.Service != 7 || back.Method != 1 || back.PayloadEncoding != "json" ||
		back.PayloadType != "event" || back.LogIDNew != "x" || !bytes.Equal(back.Payload, frame.Payload) ||
		back.Header("type") != "event" || back.Header("message_id") != "m1" {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestFrameSkipsUnknownFields(t *testing.T) {
	data := []byte{
		0x08, 0x05, // SeqID
		0x78, 0x2a, // 15: varint（未知）
		0x79, 1, 2, 3, 4, 5, 6, 7, 8, // 15: fixed64（未知）
		0x7a, 0x02, 'x', 'y', // 15: bytes（未知）
		0x7d, 1, 2, 3, 4, // 15: fixed32（未知）
		0x20, 0x01, // method
	}
	var frame Frame
	if err := frame.Unmarshal(data); err != nil || frame.SeqID != 5 || frame.Method != 1 {
		t.Fatalf("Unmarshal = %+v, %v", frame, err)
	}
}

func TestFrameRejectsMalformedInput(t *testing.T) {
	cases := map[string][]byte{
		"truncated varint":          {0x08, 0x80},
		"truncated length":          {0x42, 0x05, 'a'},
		"length overflow":           {0x42, 0xff, 0xff, 0xff, 0xff, 0x0f},
		"truncated fixed64":         {0x79, 1, 2},
		"unsupported wire type":     {0x0b},
		"truncated header contents": {0x2a, 0x03, 0x0a, 0x05, 'a'},
	}
	for name, data := range cases {
		var frame Frame
		if err := frame.Unmarshal(data); !errors.Is(err, ErrMalformedFrame) {
			t.Fatalf("%s: err = %v, want ErrMalformedFrame", name, err)
		}
	}
	var frame Frame
	if err := frame.Unmarshal(make([]byte, maxFrameBytes+1)); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("oversized frame: err = %v", err)
	}
	big := Frame{Payload: make([]byte, maxFrameBytes)}
	if _, err := big.Marshal(); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("oversized marshal: err = %v", err)
	}
}

func TestSetHeaderReplaces(t *testing.T) {
	frame := Frame{}
	frame.SetHeader("a", "1")
	frame.SetHeader("a", "2")
	frame.SetHeader("b", "3")
	if len(frame.Headers) != 2 || frame.Header("a") != "2" {
		t.Fatalf("headers = %+v", frame.Headers)
	}
}
