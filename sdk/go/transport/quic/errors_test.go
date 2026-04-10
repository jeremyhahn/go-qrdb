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

package quic

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// dialError tests
// ---------------------------------------------------------------------------

func TestDialError_Error(t *testing.T) {
	inner := errors.New("connection refused")
	err := &dialError{Err: inner}
	expected := "quic transport: dial failed: connection refused"
	if got := err.Error(); got != expected {
		t.Errorf("dialError.Error() = %q, want %q", got, expected)
	}
}

func TestDialError_ErrorDifferentMessage(t *testing.T) {
	inner := errors.New("timeout after 5s")
	err := &dialError{Err: inner}
	expected := "quic transport: dial failed: timeout after 5s"
	if got := err.Error(); got != expected {
		t.Errorf("dialError.Error() = %q, want %q", got, expected)
	}
}

func TestDialError_Unwrap(t *testing.T) {
	inner := errors.New("connection refused")
	err := &dialError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("dialError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

func TestDialError_UnwrapIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := &dialError{Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find wrapped sentinel error")
	}
}

// ---------------------------------------------------------------------------
// caFileReadError tests
// ---------------------------------------------------------------------------

func TestCaFileReadError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	err := &caFileReadError{Path: "/etc/ssl/ca.pem", Err: inner}
	expected := "quic transport: failed to read CA file /etc/ssl/ca.pem: permission denied"
	if got := err.Error(); got != expected {
		t.Errorf("caFileReadError.Error() = %q, want %q", got, expected)
	}
}

func TestCaFileReadError_ErrorDifferentPath(t *testing.T) {
	inner := errors.New("no such file")
	err := &caFileReadError{Path: "/tmp/ca-cert.pem", Err: inner}
	expected := "quic transport: failed to read CA file /tmp/ca-cert.pem: no such file"
	if got := err.Error(); got != expected {
		t.Errorf("caFileReadError.Error() = %q, want %q", got, expected)
	}
}

func TestCaFileReadError_Unwrap(t *testing.T) {
	inner := errors.New("permission denied")
	err := &caFileReadError{Path: "/etc/ssl/ca.pem", Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("caFileReadError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

func TestCaFileReadError_UnwrapIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := &caFileReadError{Path: "/ca.pem", Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find wrapped sentinel error")
	}
}

// ---------------------------------------------------------------------------
// caParseError tests
// ---------------------------------------------------------------------------

func TestCaParseError_Error(t *testing.T) {
	err := &caParseError{Path: "/etc/ssl/ca.pem"}
	expected := "quic transport: failed to parse CA certificate from /etc/ssl/ca.pem"
	if got := err.Error(); got != expected {
		t.Errorf("caParseError.Error() = %q, want %q", got, expected)
	}
}

func TestCaParseError_ErrorDifferentPath(t *testing.T) {
	err := &caParseError{Path: "/tmp/root-ca.pem"}
	expected := "quic transport: failed to parse CA certificate from /tmp/root-ca.pem"
	if got := err.Error(); got != expected {
		t.Errorf("caParseError.Error() = %q, want %q", got, expected)
	}
}

// ---------------------------------------------------------------------------
// certLoadError tests
// ---------------------------------------------------------------------------

func TestCertLoadError_Error(t *testing.T) {
	inner := errors.New("invalid PEM data")
	err := &certLoadError{Err: inner}
	expected := "quic transport: failed to load client certificate: invalid PEM data"
	if got := err.Error(); got != expected {
		t.Errorf("certLoadError.Error() = %q, want %q", got, expected)
	}
}

func TestCertLoadError_ErrorDifferentMessage(t *testing.T) {
	inner := errors.New("key pair mismatch")
	err := &certLoadError{Err: inner}
	expected := "quic transport: failed to load client certificate: key pair mismatch"
	if got := err.Error(); got != expected {
		t.Errorf("certLoadError.Error() = %q, want %q", got, expected)
	}
}

func TestCertLoadError_Unwrap(t *testing.T) {
	inner := errors.New("invalid PEM data")
	err := &certLoadError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("certLoadError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

func TestCertLoadError_UnwrapIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := &certLoadError{Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find wrapped sentinel error")
	}
}

// ---------------------------------------------------------------------------
// streamError tests
// ---------------------------------------------------------------------------

func TestStreamError_Error(t *testing.T) {
	inner := errors.New("stream limit reached")
	err := &streamError{Err: inner}
	expected := "quic transport: stream open failed: stream limit reached"
	if got := err.Error(); got != expected {
		t.Errorf("streamError.Error() = %q, want %q", got, expected)
	}
}

func TestStreamError_ErrorDifferentMessage(t *testing.T) {
	inner := errors.New("connection closed")
	err := &streamError{Err: inner}
	expected := "quic transport: stream open failed: connection closed"
	if got := err.Error(); got != expected {
		t.Errorf("streamError.Error() = %q, want %q", got, expected)
	}
}

func TestStreamError_Unwrap(t *testing.T) {
	inner := errors.New("stream limit reached")
	err := &streamError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("streamError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

func TestStreamError_UnwrapIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := &streamError{Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find wrapped sentinel error")
	}
}

// ---------------------------------------------------------------------------
// protocolError tests
// ---------------------------------------------------------------------------

func TestProtocolError_Error(t *testing.T) {
	err := &protocolError{
		Method:     "kv.Put",
		StatusCode: 1,
		Message:    "internal error",
	}
	expected := "quic transport: kv.Put failed (status 1): internal error"
	if got := err.Error(); got != expected {
		t.Errorf("protocolError.Error() = %q, want %q", got, expected)
	}
}

func TestProtocolError_ErrorDifferentValues(t *testing.T) {
	err := &protocolError{
		Method:     "admin.Health",
		StatusCode: 3,
		Message:    "unauthenticated",
	}
	expected := "quic transport: admin.Health failed (status 3): unauthenticated"
	if got := err.Error(); got != expected {
		t.Errorf("protocolError.Error() = %q, want %q", got, expected)
	}
}

func TestProtocolError_EmptyMessage(t *testing.T) {
	err := &protocolError{
		Method:     "kv.Get",
		StatusCode: 2,
		Message:    "",
	}
	expected := "quic transport: kv.Get failed (status 2): "
	if got := err.Error(); got != expected {
		t.Errorf("protocolError.Error() = %q, want %q", got, expected)
	}
}

// ---------------------------------------------------------------------------
// marshalError tests (from transport_mvcc.go)
// ---------------------------------------------------------------------------

func TestMarshalError_Error(t *testing.T) {
	inner := errors.New("proto: missing required field")
	err := &marshalError{Err: inner}
	expected := "quic: failed to marshal request: proto: missing required field"
	if got := err.Error(); got != expected {
		t.Errorf("marshalError.Error() = %q, want %q", got, expected)
	}
}

func TestMarshalError_ErrorDifferentMessage(t *testing.T) {
	inner := errors.New("invalid message type")
	err := &marshalError{Err: inner}
	expected := "quic: failed to marshal request: invalid message type"
	if got := err.Error(); got != expected {
		t.Errorf("marshalError.Error() = %q, want %q", got, expected)
	}
}

func TestMarshalError_Unwrap(t *testing.T) {
	inner := errors.New("proto: missing field")
	err := &marshalError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("marshalError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

func TestMarshalError_UnwrapIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := &marshalError{Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find wrapped sentinel error")
	}
}

// ---------------------------------------------------------------------------
// unmarshalError tests (from transport_mvcc.go)
// ---------------------------------------------------------------------------

func TestUnmarshalError_Error(t *testing.T) {
	inner := errors.New("proto: invalid wire type")
	err := &unmarshalError{Err: inner}
	expected := "quic: failed to unmarshal response: proto: invalid wire type"
	if got := err.Error(); got != expected {
		t.Errorf("unmarshalError.Error() = %q, want %q", got, expected)
	}
}

func TestUnmarshalError_ErrorDifferentMessage(t *testing.T) {
	inner := errors.New("unexpected EOF")
	err := &unmarshalError{Err: inner}
	expected := "quic: failed to unmarshal response: unexpected EOF"
	if got := err.Error(); got != expected {
		t.Errorf("unmarshalError.Error() = %q, want %q", got, expected)
	}
}

func TestUnmarshalError_Unwrap(t *testing.T) {
	inner := errors.New("proto: invalid wire type")
	err := &unmarshalError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("unmarshalError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

func TestUnmarshalError_UnwrapIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := &unmarshalError{Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find wrapped sentinel error")
	}
}

// ---------------------------------------------------------------------------
// MarshalError tests (exported, from transport_auth.go)
// ---------------------------------------------------------------------------

func TestExportedMarshalError_Error(t *testing.T) {
	inner := errors.New("json: unsupported type")
	err := &MarshalError{Err: inner}
	expected := "quic: failed to marshal request: json: unsupported type"
	if got := err.Error(); got != expected {
		t.Errorf("MarshalError.Error() = %q, want %q", got, expected)
	}
}

func TestExportedMarshalError_Unwrap(t *testing.T) {
	inner := errors.New("json: unsupported type")
	err := &MarshalError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("MarshalError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

// ---------------------------------------------------------------------------
// UnmarshalError tests (exported, from transport_auth.go)
// ---------------------------------------------------------------------------

func TestExportedUnmarshalError_Error(t *testing.T) {
	inner := errors.New("json: invalid character")
	err := &UnmarshalError{Err: inner}
	expected := "quic: failed to unmarshal response: json: invalid character"
	if got := err.Error(); got != expected {
		t.Errorf("UnmarshalError.Error() = %q, want %q", got, expected)
	}
}

func TestExportedUnmarshalError_Unwrap(t *testing.T) {
	inner := errors.New("json: invalid character")
	err := &UnmarshalError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("UnmarshalError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

// ---------------------------------------------------------------------------
// metadataMarshalError tests (from transport_vector.go)
// ---------------------------------------------------------------------------

func TestMetadataMarshalError_Error(t *testing.T) {
	inner := errors.New("json: unsupported value")
	err := &metadataMarshalError{Err: inner}
	expected := "quic transport: failed to marshal metadata: json: unsupported value"
	if got := err.Error(); got != expected {
		t.Errorf("metadataMarshalError.Error() = %q, want %q", got, expected)
	}
}

func TestMetadataMarshalError_Unwrap(t *testing.T) {
	inner := errors.New("json: unsupported value")
	err := &metadataMarshalError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("metadataMarshalError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}

// ---------------------------------------------------------------------------
// metadataUnmarshalError tests (from transport_vector.go)
// ---------------------------------------------------------------------------

func TestMetadataUnmarshalError_Error(t *testing.T) {
	inner := errors.New("json: cannot unmarshal string")
	err := &metadataUnmarshalError{Err: inner}
	expected := "quic transport: failed to unmarshal metadata: json: cannot unmarshal string"
	if got := err.Error(); got != expected {
		t.Errorf("metadataUnmarshalError.Error() = %q, want %q", got, expected)
	}
}

func TestMetadataUnmarshalError_Unwrap(t *testing.T) {
	inner := errors.New("json: cannot unmarshal string")
	err := &metadataUnmarshalError{Err: inner}
	if unwrapped := err.Unwrap(); unwrapped != inner {
		t.Errorf("metadataUnmarshalError.Unwrap() = %v, want %v", unwrapped, inner)
	}
}
