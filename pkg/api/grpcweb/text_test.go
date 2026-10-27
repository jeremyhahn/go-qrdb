package grpcweb

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

func TestTextDecoder_DecodesPaddedChunks(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"one chunk", base64.StdEncoding.EncodeToString([]byte("hello")), "hello"},
		{"padding mid-body", "aGk=" + "dGhlcmU=", "hithere"},
		{"double padding mid-body", "YQ==" + "Yg==" + "YzE=", "abc1"},
		{"line breaks", "aGVs\r\nbG8=\n", "hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := io.ReadAll(newTextDecoder(strings.NewReader(tc.in)))
			if err != nil || string(got) != tc.want {
				t.Errorf("decode(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestTextDecoder_OneByteReads(t *testing.T) {
	in := base64.StdEncoding.EncodeToString([]byte("a")) + base64.StdEncoding.EncodeToString([]byte("bcdef"))
	got, err := io.ReadAll(iotest.OneByteReader(newTextDecoder(iotest.OneByteReader(strings.NewReader(in)))))
	if err != nil || string(got) != "abcdef" {
		t.Errorf("decode = %q, %v; want abcdef", got, err)
	}
}

func TestTextDecoder_RefusesCorruptQuantum(t *testing.T) {
	got, err := io.ReadAll(newTextDecoder(strings.NewReader("aGk=a=bc")))
	var corrupt base64.CorruptInputError
	if !errors.As(err, &corrupt) || corrupt != 4 {
		t.Fatalf("error = %v, want CorruptInputError at 4", err)
	}
	if string(got) != "hi" {
		t.Errorf("decoded before the error = %q, want hi", got)
	}
}

func TestTextDecoder_RefusesTruncatedQuantum(t *testing.T) {
	_, err := io.ReadAll(newTextDecoder(strings.NewReader("aGk=aG")))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestTextDecoder_PassesSourceError(t *testing.T) {
	_, err := io.ReadAll(newTextDecoder(iotest.ErrReader(io.ErrClosedPipe)))
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("error = %v, want io.ErrClosedPipe", err)
	}
}

func TestCompleteFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want int
	}{
		{"empty", nil, 0},
		{"partial header", []byte{0, 0, 0}, 0},
		{"empty message", []byte{0, 0, 0, 0, 0}, 5},
		{"message", []byte{0, 0, 0, 0, 2, 'h', 'i'}, 7},
		{"message then partial", []byte{0, 0, 0, 0, 1, 'a', 0, 0, 0, 0, 3, 'b'}, 6},
		{"two messages", []byte{0, 0, 0, 0, 1, 'a', 0x80, 0, 0, 0, 1, 'b'}, 12},
		{"length beyond body", []byte{0, 0xff, 0xff, 0xff, 0xff}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := completeFrames(tc.in); got != tc.want {
				t.Errorf("completeFrames(%x) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestResponseWriter_TextEncodesWholeMessages(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{w: rec, rc: http.NewResponseController(rec), header: http.Header{"Content-Type": {"application/grpc"}}, contentType: "application/grpc-web-text+proto", text: true}
	message := []byte{0, 0, 0, 0, 2, 'h', 'i'}
	for _, part := range [][]byte{message[:5], message[5:6], message[6:]} {
		if n, err := rw.Write(part); n != len(part) || err != nil {
			t.Fatalf("Write(%x) = %d, %v", part, n, err)
		}
		if part[0] != 'i' && rec.Body.Len() != 0 {
			t.Fatalf("body after %x = %q, want nothing before the message completes", part, rec.Body.String())
		}
	}
	if got, want := rec.Body.String(), base64.StdEncoding.EncodeToString(message); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestResponseWriter_FinishSendsHeldBackBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{w: rec, rc: http.NewResponseController(rec), header: http.Header{"Content-Type": {"application/grpc"}}, contentType: "application/grpc-web-text+proto", text: true}
	if _, err := rw.Write([]byte{0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	rw.header.Set(http.TrailerPrefix+"Grpc-Status", "0")
	rw.finish()
	trailer := append([]byte{trailerFlag, 0, 0, 0, 16}, "grpc-status: 0\r\n"...)
	want := base64.StdEncoding.EncodeToString([]byte{0, 0, 0}) + base64.StdEncoding.EncodeToString(trailer)
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
