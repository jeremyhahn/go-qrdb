// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package remote

import (
	"errors"
	"testing"
)

var errTestCause = errors.New("boom")

func TestIndexError_ErrorAndUnwrap(t *testing.T) {
	err := &IndexError{Op: "insert id=42", Cause: errTestCause}
	if got, want := err.Error(), "remote gpu index: insert id=42: boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, errTestCause) {
		t.Fatal("errors.Is did not reach the cause")
	}
}

func TestTLSConfigError_ErrorAndUnwrap(t *testing.T) {
	err := &TLSConfigError{Op: "load client key pair", Cause: errTestCause}
	if got, want := err.Error(), "load client key pair: boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, errTestCause) {
		t.Fatal("errors.Is did not reach the cause")
	}
}

func TestCACertParseError_Error(t *testing.T) {
	err := &CACertParseError{Path: "/tmp/ca.pem"}
	if got, want := err.Error(), "parse CA cert /tmp/ca.pem: no valid certificates found"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestMarshalError_ErrorAndUnwrap(t *testing.T) {
	err := &MarshalError{Method: "/svc/Insert", Cause: errTestCause}
	if got, want := err.Error(), "marshal request for /svc/Insert: boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, errTestCause) {
		t.Fatal("errors.Is did not reach the cause")
	}
}

func TestUnmarshalError_ErrorAndUnwrap(t *testing.T) {
	err := &UnmarshalError{Method: "/svc/Search", Cause: errTestCause}
	if got, want := err.Error(), "unmarshal response for /svc/Search: boom"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, errTestCause) {
		t.Fatal("errors.Is did not reach the cause")
	}
}

func TestNewRemoteGPUIndex_DialOptionsErrorWrapped(t *testing.T) {
	idx, err := NewRemoteGPUIndex(&Config{
		Endpoint: "host:9090",
		TLSCA:    "/nonexistent/ca.pem",
	})
	if idx != nil {
		t.Fatal("expected nil index on dial option failure")
	}
	var ie *IndexError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *IndexError, got %T: %v", err, err)
	}
	if ie.Op != "build dial options" {
		t.Fatalf("Op = %q, want %q", ie.Op, "build dial options")
	}
}
