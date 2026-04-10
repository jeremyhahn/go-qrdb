// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

// errForcedWrite is a sentinel used to simulate write failures.
var errForcedWrite = errors.New("forced write error")

// failWriter always returns an error on Write.
type failWriter struct{}

func (fw *failWriter) Write(p []byte) (int, error) {
	return 0, errForcedWrite
}

// limitedWriter writes the first N bytes successfully, then fails.
type limitedWriter struct {
	buf       bytes.Buffer
	remaining int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.remaining <= 0 {
		return 0, errForcedWrite
	}
	if len(p) <= lw.remaining {
		lw.remaining -= len(p)
		return lw.buf.Write(p)
	}
	n, err := lw.buf.Write(p[:lw.remaining])
	lw.remaining = 0
	if err != nil {
		return n, err
	}
	return n, errForcedWrite
}

// ---------------------------------------------------------------------------
// WriteRequest tests
// ---------------------------------------------------------------------------

func TestWriteRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	method := "kv.Put"
	var requestID uint64 = 42
	payload := []byte(`{"key":"foo","value":"bar"}`)

	if err := WriteRequest(&buf, method, requestID, payload); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	gotMethod, gotID, gotPayload, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if gotMethod != method {
		t.Errorf("method = %q, want %q", gotMethod, method)
	}
	if gotID != requestID {
		t.Errorf("requestID = %d, want %d", gotID, requestID)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("payload = %q, want %q", gotPayload, payload)
	}
}

func TestWriteRequestEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	method := "kv.Get"
	var requestID uint64 = 1

	if err := WriteRequest(&buf, method, requestID, nil); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	gotMethod, gotID, gotPayload, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if gotMethod != method {
		t.Errorf("method = %q, want %q", gotMethod, method)
	}
	if gotID != requestID {
		t.Errorf("requestID = %d, want %d", gotID, requestID)
	}
	if gotPayload != nil {
		t.Errorf("payload = %v, want nil", gotPayload)
	}
}

func TestWriteRequestEmptyMethod(t *testing.T) {
	var buf bytes.Buffer
	var requestID uint64 = 99

	if err := WriteRequest(&buf, "", requestID, []byte("data")); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	gotMethod, gotID, gotPayload, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if gotMethod != "" {
		t.Errorf("method = %q, want empty string", gotMethod)
	}
	if gotID != requestID {
		t.Errorf("requestID = %d, want %d", gotID, requestID)
	}
	if !bytes.Equal(gotPayload, []byte("data")) {
		t.Errorf("payload = %q, want %q", gotPayload, "data")
	}
}

func TestWriteRequestMaxMethodLen(t *testing.T) {
	var buf bytes.Buffer
	method := strings.Repeat("x", MaxMethodLen)
	var requestID uint64 = 7

	if err := WriteRequest(&buf, method, requestID, nil); err != nil {
		t.Fatalf("WriteRequest() error = %v, want nil for method at max length", err)
	}

	gotMethod, gotID, _, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if gotMethod != method {
		t.Errorf("method length = %d, want %d", len(gotMethod), MaxMethodLen)
	}
	if gotID != requestID {
		t.Errorf("requestID = %d, want %d", gotID, requestID)
	}
}

func TestWriteRequestMethodTooLong(t *testing.T) {
	var buf bytes.Buffer
	method := strings.Repeat("a", MaxMethodLen+1)

	err := WriteRequest(&buf, method, 1, nil)
	if err == nil {
		t.Fatal("WriteRequest() expected error for oversized method, got nil")
	}
	var mtlErr *MethodTooLongError
	if !errors.As(err, &mtlErr) {
		t.Fatalf("error type = %T, want *MethodTooLongError", err)
	}
	if mtlErr.Length != MaxMethodLen+1 {
		t.Errorf("MethodTooLongError.Length = %d, want %d", mtlErr.Length, MaxMethodLen+1)
	}
	if mtlErr.Max != MaxMethodLen {
		t.Errorf("MethodTooLongError.Max = %d, want %d", mtlErr.Max, MaxMethodLen)
	}
}

func TestWriteRequestPayloadTooLarge(t *testing.T) {
	var buf bytes.Buffer
	oversized := make([]byte, MaxPayloadSize+1)

	err := WriteRequest(&buf, "kv.Put", 1, oversized)
	if err == nil {
		t.Fatal("WriteRequest() expected error for oversized payload, got nil")
	}
	var ptlErr *PayloadTooLargeError
	if !errors.As(err, &ptlErr) {
		t.Fatalf("error type = %T, want *PayloadTooLargeError", err)
	}
	if ptlErr.Size != MaxPayloadSize+1 {
		t.Errorf("PayloadTooLargeError.Size = %d, want %d", ptlErr.Size, MaxPayloadSize+1)
	}
	if ptlErr.Max != MaxPayloadSize {
		t.Errorf("PayloadTooLargeError.Max = %d, want %d", ptlErr.Max, MaxPayloadSize)
	}
}

func TestWriteRequestHeaderWriteError(t *testing.T) {
	fw := &failWriter{}

	err := WriteRequest(fw, "kv.Put", 1, []byte("data"))
	if err == nil {
		t.Fatal("WriteRequest() expected error on write failure, got nil")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("error type = %T, want *WriteError", err)
	}
	if !errors.Is(we.Err, errForcedWrite) {
		t.Errorf("underlying error = %v, want %v", we.Err, errForcedWrite)
	}
}

func TestWriteRequestPayloadWriteError(t *testing.T) {
	// Allow the header to succeed, then fail on the payload write.
	// Header size for method "kv.Put" (6 bytes): 2 + 6 + 8 + 4 = 20 bytes.
	lw := &limitedWriter{remaining: 20}

	err := WriteRequest(lw, "kv.Put", 1, []byte("payload-data"))
	if err == nil {
		t.Fatal("WriteRequest() expected error on payload write failure, got nil")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("error type = %T, want *WriteError", err)
	}
}

func TestWriteRequestMaxRequestID(t *testing.T) {
	var buf bytes.Buffer
	maxID := ^uint64(0)

	if err := WriteRequest(&buf, "test", maxID, nil); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	_, gotID, _, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if gotID != maxID {
		t.Errorf("requestID = %d, want %d", gotID, maxID)
	}
}

func TestWriteRequestZeroRequestID(t *testing.T) {
	var buf bytes.Buffer

	if err := WriteRequest(&buf, "method", 0, []byte("p")); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	_, gotID, _, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if gotID != 0 {
		t.Errorf("requestID = %d, want 0", gotID)
	}
}

// ---------------------------------------------------------------------------
// ReadRequest tests
// ---------------------------------------------------------------------------

func TestReadRequestEmptyReader(t *testing.T) {
	r := bytes.NewReader(nil)

	_, _, _, err := ReadRequest(r)
	if err == nil {
		t.Fatal("ReadRequest() expected error on empty reader, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadRequestTruncatedMethodLen(t *testing.T) {
	// Only 1 byte when 2 are needed for methodLen.
	r := bytes.NewReader([]byte{0x00})

	_, _, _, err := ReadRequest(r)
	if err == nil {
		t.Fatal("ReadRequest() expected error on truncated method length, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadRequestTruncatedMethodName(t *testing.T) {
	// Method length says 10, but only 3 bytes of method name follow.
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint16(10))
	frame.Write([]byte("abc"))

	_, _, _, err := ReadRequest(&frame)
	if err == nil {
		t.Fatal("ReadRequest() expected error on truncated method name, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadRequestTruncatedRequestID(t *testing.T) {
	// Valid method header, but not enough bytes for requestID.
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint16(3))
	frame.Write([]byte("get"))
	frame.Write([]byte{0x01, 0x02}) // only 2 of 8 bytes

	_, _, _, err := ReadRequest(&frame)
	if err == nil {
		t.Fatal("ReadRequest() expected error on truncated request ID, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadRequestTruncatedPayloadLen(t *testing.T) {
	// Valid method + requestID, but not enough bytes for payloadLen.
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint16(3))
	frame.Write([]byte("get"))
	_ = binary.Write(&frame, binary.BigEndian, uint64(1))
	frame.Write([]byte{0x00, 0x00}) // only 2 of 4 bytes

	_, _, _, err := ReadRequest(&frame)
	if err == nil {
		t.Fatal("ReadRequest() expected error on truncated payload length, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadRequestTruncatedPayload(t *testing.T) {
	// Valid header claiming 100 bytes of payload, but only 5 present.
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint16(3))
	frame.Write([]byte("put"))
	_ = binary.Write(&frame, binary.BigEndian, uint64(1))
	_ = binary.Write(&frame, binary.BigEndian, uint32(100))
	frame.Write([]byte("short"))

	_, _, _, err := ReadRequest(&frame)
	if err == nil {
		t.Fatal("ReadRequest() expected error on truncated payload, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadRequestMethodTooLongOnWire(t *testing.T) {
	// Craft a frame where the encoded methodLen exceeds MaxMethodLen.
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint16(MaxMethodLen+1))
	// We don't need to write actual method bytes; the check happens first.

	_, _, _, err := ReadRequest(&frame)
	if err == nil {
		t.Fatal("ReadRequest() expected error for oversized method on wire, got nil")
	}
	var mtlErr *MethodTooLongError
	if !errors.As(err, &mtlErr) {
		t.Fatalf("error type = %T, want *MethodTooLongError", err)
	}
	if mtlErr.Length != MaxMethodLen+1 {
		t.Errorf("MethodTooLongError.Length = %d, want %d", mtlErr.Length, MaxMethodLen+1)
	}
}

func TestReadRequestPayloadTooLargeOnWire(t *testing.T) {
	// Craft a frame with a valid header but payloadLen exceeding MaxPayloadSize.
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint16(3))
	frame.Write([]byte("put"))
	_ = binary.Write(&frame, binary.BigEndian, uint64(1))
	_ = binary.Write(&frame, binary.BigEndian, uint32(MaxPayloadSize+1))

	_, _, _, err := ReadRequest(&frame)
	if err == nil {
		t.Fatal("ReadRequest() expected error for oversized payload on wire, got nil")
	}
	var ptlErr *PayloadTooLargeError
	if !errors.As(err, &ptlErr) {
		t.Fatalf("error type = %T, want *PayloadTooLargeError", err)
	}
}

// ---------------------------------------------------------------------------
// WriteResponse tests
// ---------------------------------------------------------------------------

func TestWriteResponseRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	var requestID uint64 = 100
	statusCode := StatusOK
	payload := []byte(`{"result":"success"}`)

	if err := WriteResponse(&buf, requestID, statusCode, payload); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}

	gotID, gotStatus, gotPayload, err := ReadResponse(&buf)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if gotID != requestID {
		t.Errorf("requestID = %d, want %d", gotID, requestID)
	}
	if gotStatus != statusCode {
		t.Errorf("statusCode = %d, want %d", gotStatus, statusCode)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("payload = %q, want %q", gotPayload, payload)
	}
}

func TestWriteResponseEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	var requestID uint64 = 5
	statusCode := StatusError

	if err := WriteResponse(&buf, requestID, statusCode, nil); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}

	gotID, gotStatus, gotPayload, err := ReadResponse(&buf)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if gotID != requestID {
		t.Errorf("requestID = %d, want %d", gotID, requestID)
	}
	if gotStatus != statusCode {
		t.Errorf("statusCode = %d, want %d", gotStatus, statusCode)
	}
	if gotPayload != nil {
		t.Errorf("payload = %v, want nil", gotPayload)
	}
}

func TestWriteResponseAllStatusCodes(t *testing.T) {
	codes := []uint16{
		StatusOK,
		StatusError,
		StatusUnimplemented,
		StatusUnauthenticated,
		StatusPermissionDenied,
	}
	for _, code := range codes {
		var buf bytes.Buffer
		if err := WriteResponse(&buf, 1, code, nil); err != nil {
			t.Fatalf("WriteResponse(statusCode=%d) error = %v", code, err)
		}
		_, gotCode, _, err := ReadResponse(&buf)
		if err != nil {
			t.Fatalf("ReadResponse(statusCode=%d) error = %v", code, err)
		}
		if gotCode != code {
			t.Errorf("statusCode = %d, want %d", gotCode, code)
		}
	}
}

func TestWriteResponsePayloadTooLarge(t *testing.T) {
	var buf bytes.Buffer
	oversized := make([]byte, MaxPayloadSize+1)

	err := WriteResponse(&buf, 1, StatusOK, oversized)
	if err == nil {
		t.Fatal("WriteResponse() expected error for oversized payload, got nil")
	}
	var ptlErr *PayloadTooLargeError
	if !errors.As(err, &ptlErr) {
		t.Fatalf("error type = %T, want *PayloadTooLargeError", err)
	}
	if ptlErr.Size != MaxPayloadSize+1 {
		t.Errorf("PayloadTooLargeError.Size = %d, want %d", ptlErr.Size, MaxPayloadSize+1)
	}
}

func TestWriteResponseHeaderWriteError(t *testing.T) {
	fw := &failWriter{}

	err := WriteResponse(fw, 1, StatusOK, []byte("data"))
	if err == nil {
		t.Fatal("WriteResponse() expected error on write failure, got nil")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("error type = %T, want *WriteError", err)
	}
	if !errors.Is(we.Err, errForcedWrite) {
		t.Errorf("underlying error = %v, want %v", we.Err, errForcedWrite)
	}
}

func TestWriteResponsePayloadWriteError(t *testing.T) {
	// Response header is 14 bytes. Allow that, fail on payload.
	lw := &limitedWriter{remaining: 14}

	err := WriteResponse(lw, 1, StatusOK, []byte("payload-data"))
	if err == nil {
		t.Fatal("WriteResponse() expected error on payload write failure, got nil")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("error type = %T, want *WriteError", err)
	}
}

func TestWriteResponseMaxRequestID(t *testing.T) {
	var buf bytes.Buffer
	maxID := ^uint64(0)

	if err := WriteResponse(&buf, maxID, StatusOK, nil); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}

	gotID, _, _, err := ReadResponse(&buf)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if gotID != maxID {
		t.Errorf("requestID = %d, want %d", gotID, maxID)
	}
}

// ---------------------------------------------------------------------------
// ReadResponse tests
// ---------------------------------------------------------------------------

func TestReadResponseEmptyReader(t *testing.T) {
	r := bytes.NewReader(nil)

	_, _, _, err := ReadResponse(r)
	if err == nil {
		t.Fatal("ReadResponse() expected error on empty reader, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadResponseTruncatedHeader(t *testing.T) {
	// Response header needs 14 bytes, provide only 10.
	r := bytes.NewReader(make([]byte, 10))

	_, _, _, err := ReadResponse(r)
	if err == nil {
		t.Fatal("ReadResponse() expected error on truncated header, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadResponseTruncatedPayload(t *testing.T) {
	// Valid 14-byte header claiming 50 bytes of payload, but only 3 present.
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint64(1))
	_ = binary.Write(&frame, binary.BigEndian, uint16(StatusOK))
	_ = binary.Write(&frame, binary.BigEndian, uint32(50))
	frame.Write([]byte("abc"))

	_, _, _, err := ReadResponse(&frame)
	if err == nil {
		t.Fatal("ReadResponse() expected error on truncated payload, got nil")
	}
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *ReadError", err)
	}
}

func TestReadResponsePayloadTooLargeOnWire(t *testing.T) {
	// Craft a response header with payloadLen exceeding MaxPayloadSize.
	var frame bytes.Buffer
	_ = binary.Write(&frame, binary.BigEndian, uint64(1))
	_ = binary.Write(&frame, binary.BigEndian, uint16(StatusOK))
	_ = binary.Write(&frame, binary.BigEndian, uint32(MaxPayloadSize+1))

	_, _, _, err := ReadResponse(&frame)
	if err == nil {
		t.Fatal("ReadResponse() expected error for oversized payload on wire, got nil")
	}
	var ptlErr *PayloadTooLargeError
	if !errors.As(err, &ptlErr) {
		t.Fatalf("error type = %T, want *PayloadTooLargeError", err)
	}
}

// ---------------------------------------------------------------------------
// Multiple frames on a single stream
// ---------------------------------------------------------------------------

func TestMultipleRequestFramesOnStream(t *testing.T) {
	var buf bytes.Buffer

	methods := []string{"kv.Put", "kv.Get", "kv.Delete"}
	for i, m := range methods {
		if err := WriteRequest(&buf, m, uint64(i+1), []byte(m)); err != nil {
			t.Fatalf("WriteRequest(%q) error = %v", m, err)
		}
	}

	for i, wantMethod := range methods {
		gotMethod, gotID, gotPayload, err := ReadRequest(&buf)
		if err != nil {
			t.Fatalf("ReadRequest() frame %d error = %v", i, err)
		}
		if gotMethod != wantMethod {
			t.Errorf("frame %d: method = %q, want %q", i, gotMethod, wantMethod)
		}
		if gotID != uint64(i+1) {
			t.Errorf("frame %d: requestID = %d, want %d", i, gotID, i+1)
		}
		if string(gotPayload) != wantMethod {
			t.Errorf("frame %d: payload = %q, want %q", i, gotPayload, wantMethod)
		}
	}
}

func TestMultipleResponseFramesOnStream(t *testing.T) {
	var buf bytes.Buffer

	statuses := []uint16{StatusOK, StatusError, StatusUnimplemented}
	for i, s := range statuses {
		if err := WriteResponse(&buf, uint64(i+1), s, []byte{byte(i)}); err != nil {
			t.Fatalf("WriteResponse(%d) error = %v", s, err)
		}
	}

	for i, wantStatus := range statuses {
		gotID, gotStatus, gotPayload, err := ReadResponse(&buf)
		if err != nil {
			t.Fatalf("ReadResponse() frame %d error = %v", i, err)
		}
		if gotID != uint64(i+1) {
			t.Errorf("frame %d: requestID = %d, want %d", i, gotID, i+1)
		}
		if gotStatus != wantStatus {
			t.Errorf("frame %d: statusCode = %d, want %d", i, gotStatus, wantStatus)
		}
		if len(gotPayload) != 1 || gotPayload[0] != byte(i) {
			t.Errorf("frame %d: payload = %v, want [%d]", i, gotPayload, i)
		}
	}
}

// ---------------------------------------------------------------------------
// Wire format verification (byte-level)
// ---------------------------------------------------------------------------

func TestWriteRequestWireFormat(t *testing.T) {
	var buf bytes.Buffer
	method := "ab"
	var requestID uint64 = 0x0102030405060708
	payload := []byte{0xAA, 0xBB}

	if err := WriteRequest(&buf, method, requestID, payload); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	data := buf.Bytes()

	// methodLen: 2 bytes big-endian = 2
	gotMethodLen := binary.BigEndian.Uint16(data[0:2])
	if gotMethodLen != 2 {
		t.Errorf("wire methodLen = %d, want 2", gotMethodLen)
	}

	// method: "ab"
	if string(data[2:4]) != "ab" {
		t.Errorf("wire method = %q, want %q", string(data[2:4]), "ab")
	}

	// requestID: 8 bytes
	gotRID := binary.BigEndian.Uint64(data[4:12])
	if gotRID != requestID {
		t.Errorf("wire requestID = %#x, want %#x", gotRID, requestID)
	}

	// payloadLen: 4 bytes = 2
	gotPLen := binary.BigEndian.Uint32(data[12:16])
	if gotPLen != 2 {
		t.Errorf("wire payloadLen = %d, want 2", gotPLen)
	}

	// payload: 0xAA, 0xBB
	if !bytes.Equal(data[16:18], payload) {
		t.Errorf("wire payload = %x, want %x", data[16:18], payload)
	}

	// Total length
	if len(data) != 18 {
		t.Errorf("total frame size = %d, want 18", len(data))
	}
}

func TestWriteResponseWireFormat(t *testing.T) {
	var buf bytes.Buffer
	var requestID uint64 = 0x0A0B0C0D0E0F1011
	statusCode := StatusPermissionDenied
	payload := []byte{0xFF}

	if err := WriteResponse(&buf, requestID, statusCode, payload); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}

	data := buf.Bytes()

	// requestID: 8 bytes
	gotRID := binary.BigEndian.Uint64(data[0:8])
	if gotRID != requestID {
		t.Errorf("wire requestID = %#x, want %#x", gotRID, requestID)
	}

	// statusCode: 2 bytes
	gotStatus := binary.BigEndian.Uint16(data[8:10])
	if gotStatus != StatusPermissionDenied {
		t.Errorf("wire statusCode = %d, want %d", gotStatus, StatusPermissionDenied)
	}

	// payloadLen: 4 bytes = 1
	gotPLen := binary.BigEndian.Uint32(data[10:14])
	if gotPLen != 1 {
		t.Errorf("wire payloadLen = %d, want 1", gotPLen)
	}

	// payload: 0xFF
	if data[14] != 0xFF {
		t.Errorf("wire payload = %x, want ff", data[14])
	}

	// Total length: 14 header + 1 payload
	if len(data) != 15 {
		t.Errorf("total frame size = %d, want 15", len(data))
	}
}

// ---------------------------------------------------------------------------
// Constants tests
// ---------------------------------------------------------------------------

func TestStatusCodeConstants(t *testing.T) {
	if StatusOK != 0 {
		t.Errorf("StatusOK = %d, want 0", StatusOK)
	}
	if StatusError != 1 {
		t.Errorf("StatusError = %d, want 1", StatusError)
	}
	if StatusUnimplemented != 2 {
		t.Errorf("StatusUnimplemented = %d, want 2", StatusUnimplemented)
	}
	if StatusUnauthenticated != 3 {
		t.Errorf("StatusUnauthenticated = %d, want 3", StatusUnauthenticated)
	}
	if StatusPermissionDenied != 4 {
		t.Errorf("StatusPermissionDenied = %d, want 4", StatusPermissionDenied)
	}
}

func TestMaxConstants(t *testing.T) {
	if MaxMethodLen != 256 {
		t.Errorf("MaxMethodLen = %d, want 256", MaxMethodLen)
	}
	if MaxPayloadSize != 64*1024*1024 {
		t.Errorf("MaxPayloadSize = %d, want %d", MaxPayloadSize, 64*1024*1024)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestMethodTooLongErrorMessage(t *testing.T) {
	err := &MethodTooLongError{Length: 300, Max: 256}
	got := err.Error()
	if !strings.Contains(got, "method name length") {
		t.Errorf("Error() = %q, want it to contain %q", got, "method name length")
	}
	if !strings.Contains(got, "300") {
		t.Errorf("Error() = %q, want it to contain %q", got, "300")
	}
	if !strings.Contains(got, "256") {
		t.Errorf("Error() = %q, want it to contain %q", got, "256")
	}
}

func TestMethodTooLongErrorWithDifferentValues(t *testing.T) {
	err := &MethodTooLongError{Length: 1000, Max: 500}
	got := err.Error()
	if !strings.Contains(got, "1000") {
		t.Errorf("Error() = %q, want it to contain %q", got, "1000")
	}
	if !strings.Contains(got, "500") {
		t.Errorf("Error() = %q, want it to contain %q", got, "500")
	}
}

func TestMethodTooLongErrorImplementsError(t *testing.T) {
	err := &MethodTooLongError{Length: 1, Max: 0}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

func TestPayloadTooLargeErrorMessage(t *testing.T) {
	err := &PayloadTooLargeError{Size: 100000000, Max: 67108864}
	got := err.Error()
	if !strings.Contains(got, "payload size") {
		t.Errorf("Error() = %q, want it to contain %q", got, "payload size")
	}
	if !strings.Contains(got, "100000000") {
		t.Errorf("Error() = %q, want it to contain %q", got, "100000000")
	}
	if !strings.Contains(got, "67108864") {
		t.Errorf("Error() = %q, want it to contain %q", got, "67108864")
	}
}

func TestPayloadTooLargeErrorWithDifferentValues(t *testing.T) {
	err := &PayloadTooLargeError{Size: 5000, Max: 1024}
	got := err.Error()
	if !strings.Contains(got, "5000") {
		t.Errorf("Error() = %q, want it to contain %q", got, "5000")
	}
	if !strings.Contains(got, "1024") {
		t.Errorf("Error() = %q, want it to contain %q", got, "1024")
	}
}

func TestPayloadTooLargeErrorImplementsError(t *testing.T) {
	err := &PayloadTooLargeError{Size: 1, Max: 0}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

func TestReadErrorMessage(t *testing.T) {
	underlying := errors.New("connection reset")
	err := &ReadError{Err: underlying}
	got := err.Error()
	if !strings.Contains(got, "read error") {
		t.Errorf("Error() = %q, want it to contain %q", got, "read error")
	}
	if !strings.Contains(got, "connection reset") {
		t.Errorf("Error() = %q, want it to contain %q", got, "connection reset")
	}
}

func TestReadErrorUnwrap(t *testing.T) {
	underlying := errors.New("io timeout")
	err := &ReadError{Err: underlying}
	unwrapped := err.Unwrap()
	if unwrapped != underlying {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, underlying)
	}
}

func TestReadErrorUnwrapWithErrorsIs(t *testing.T) {
	underlying := io.EOF
	err := &ReadError{Err: underlying}
	if !errors.Is(err, io.EOF) {
		t.Errorf("errors.Is(ReadError, io.EOF) = false, want true")
	}
}

func TestReadErrorImplementsError(t *testing.T) {
	err := &ReadError{Err: errors.New("test")}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

func TestWriteErrorMessage(t *testing.T) {
	underlying := errors.New("broken pipe")
	err := &WriteError{Err: underlying}
	got := err.Error()
	if !strings.Contains(got, "write error") {
		t.Errorf("Error() = %q, want it to contain %q", got, "write error")
	}
	if !strings.Contains(got, "broken pipe") {
		t.Errorf("Error() = %q, want it to contain %q", got, "broken pipe")
	}
}

func TestWriteErrorUnwrap(t *testing.T) {
	underlying := errors.New("disk full")
	err := &WriteError{Err: underlying}
	unwrapped := err.Unwrap()
	if unwrapped != underlying {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, underlying)
	}
}

func TestWriteErrorUnwrapWithErrorsIs(t *testing.T) {
	underlying := io.ErrClosedPipe
	err := &WriteError{Err: underlying}
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("errors.Is(WriteError, io.ErrClosedPipe) = false, want true")
	}
}

func TestWriteErrorImplementsError(t *testing.T) {
	err := &WriteError{Err: errors.New("test")}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

// ---------------------------------------------------------------------------
// Edge case: LimitReader for precise truncation
// ---------------------------------------------------------------------------

func TestReadRequestWithLimitReader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteRequest(&buf, "kv.Put", 42, []byte("hello")); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	// Truncate at various points to verify ReadRequest handles partial reads.
	fullFrame := buf.Bytes()
	for _, limit := range []int64{0, 1, 2, 5, 10, 14, int64(len(fullFrame) - 1)} {
		lr := io.LimitReader(bytes.NewReader(fullFrame), limit)
		_, _, _, err := ReadRequest(lr)
		if err == nil {
			t.Errorf("ReadRequest() with limit=%d expected error, got nil", limit)
		}
	}
}

func TestReadResponseWithLimitReader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteResponse(&buf, 42, StatusOK, []byte("world")); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}

	fullFrame := buf.Bytes()
	for _, limit := range []int64{0, 1, 7, 13, int64(len(fullFrame) - 1)} {
		lr := io.LimitReader(bytes.NewReader(fullFrame), limit)
		_, _, _, err := ReadResponse(lr)
		if err == nil {
			t.Errorf("ReadResponse() with limit=%d expected error, got nil", limit)
		}
	}
}

// ---------------------------------------------------------------------------
// Edge case: single-byte payload
// ---------------------------------------------------------------------------

func TestWriteRequestSingleBytePayload(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte{0x42}

	if err := WriteRequest(&buf, "m", 1, payload); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	_, _, gotPayload, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("payload = %v, want %v", gotPayload, payload)
	}
}

func TestWriteResponseSingleBytePayload(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte{0xFF}

	if err := WriteResponse(&buf, 1, StatusOK, payload); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}

	_, _, gotPayload, err := ReadResponse(&buf)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("payload = %v, want %v", gotPayload, payload)
	}
}

// ---------------------------------------------------------------------------
// Edge case: empty []byte vs nil payload
// ---------------------------------------------------------------------------

func TestWriteRequestEmptySlicePayload(t *testing.T) {
	var buf bytes.Buffer

	if err := WriteRequest(&buf, "m", 1, []byte{}); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	_, _, gotPayload, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	// An empty payload is read back as nil (payloadLen == 0 skips allocation).
	if gotPayload != nil {
		t.Errorf("payload = %v, want nil for zero-length payload", gotPayload)
	}
}

func TestWriteResponseEmptySlicePayload(t *testing.T) {
	var buf bytes.Buffer

	if err := WriteResponse(&buf, 1, StatusOK, []byte{}); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}

	_, _, gotPayload, err := ReadResponse(&buf)
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	if gotPayload != nil {
		t.Errorf("payload = %v, want nil for zero-length payload", gotPayload)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkWriteRequestReadRequest(b *testing.B) {
	payload := bytes.Repeat([]byte("x"), 1024)
	var buf bytes.Buffer
	buf.Grow(2 + 6 + 8 + 4 + len(payload))

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		buf.Reset()
		if err := WriteRequest(&buf, "kv.Put", 12345, payload); err != nil {
			b.Fatal(err)
		}
		if _, _, _, err := ReadRequest(&buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteResponseReadResponse(b *testing.B) {
	payload := bytes.Repeat([]byte("y"), 1024)
	var buf bytes.Buffer
	buf.Grow(14 + len(payload))

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		buf.Reset()
		if err := WriteResponse(&buf, 12345, StatusOK, payload); err != nil {
			b.Fatal(err)
		}
		if _, _, _, err := ReadResponse(&buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteRequestSmallPayload(b *testing.B) {
	payload := []byte("v")
	var buf bytes.Buffer

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		buf.Reset()
		if err := WriteRequest(&buf, "g", 1, payload); err != nil {
			b.Fatal(err)
		}
		if _, _, _, err := ReadRequest(&buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteResponseNoPayload(b *testing.B) {
	var buf bytes.Buffer

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		buf.Reset()
		if err := WriteResponse(&buf, 1, StatusOK, nil); err != nil {
			b.Fatal(err)
		}
		if _, _, _, err := ReadResponse(&buf); err != nil {
			b.Fatal(err)
		}
	}
}
