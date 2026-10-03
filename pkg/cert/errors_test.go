// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cert

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// CertNotFoundError
// ---------------------------------------------------------------------------

func TestCertNotFoundError_Message(t *testing.T) {
	err := &CertNotFoundError{Serial: "abc123"}
	if !strings.Contains(err.Error(), "abc123") {
		t.Errorf("expected serial in error, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %s", err.Error())
	}
}

func TestCertNotFoundError_As(t *testing.T) {
	var err error = &CertNotFoundError{Serial: "s1"}
	var target *CertNotFoundError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *CertNotFoundError")
	}
	if target.Serial != "s1" {
		t.Fatalf("Serial: got %q, want %q", target.Serial, "s1")
	}
}

// ---------------------------------------------------------------------------
// CertRevokedError
// ---------------------------------------------------------------------------

func TestCertRevokedError_Message(t *testing.T) {
	err := &CertRevokedError{Serial: "def456"}
	if !strings.Contains(err.Error(), "def456") {
		t.Errorf("expected serial in error, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("expected 'revoked' in error, got: %s", err.Error())
	}
}

func TestCertRevokedError_As(t *testing.T) {
	var err error = &CertRevokedError{Serial: "rev1"}
	var target *CertRevokedError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *CertRevokedError")
	}
	if target.Serial != "rev1" {
		t.Fatalf("Serial: got %q, want %q", target.Serial, "rev1")
	}
}

// ---------------------------------------------------------------------------
// CertGenerateError
// ---------------------------------------------------------------------------

func TestCertGenerateError_Message(t *testing.T) {
	err := &CertGenerateError{Err: io.ErrUnexpectedEOF}
	msg := err.Error()
	if !strings.Contains(msg, "generation failed") {
		t.Errorf("expected 'generation failed' in error, got: %s", msg)
	}
	if !strings.Contains(msg, "unexpected EOF") {
		t.Errorf("expected cause in error, got: %s", msg)
	}
}

func TestCertGenerateError_NilErr(t *testing.T) {
	err := &CertGenerateError{Err: nil}
	msg := err.Error()
	if !strings.Contains(msg, "generation failed") {
		t.Errorf("expected 'generation failed' in error, got: %s", msg)
	}
	if err.Unwrap() != nil {
		t.Error("Unwrap should return nil for nil Err")
	}
}

func TestCertGenerateError_Unwrap(t *testing.T) {
	inner := io.ErrClosedPipe
	err := &CertGenerateError{Err: inner}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should allow errors.Is to match the inner error")
	}
	var target *CertGenerateError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *CertGenerateError")
	}
}

// ---------------------------------------------------------------------------
// CertExportError
// ---------------------------------------------------------------------------

func TestCertExportError_Message(t *testing.T) {
	err := &CertExportError{Err: io.ErrShortWrite}
	msg := err.Error()
	if !strings.Contains(msg, "export failed") {
		t.Errorf("expected 'export failed' in error, got: %s", msg)
	}
	if !strings.Contains(msg, "short write") {
		t.Errorf("expected cause in error, got: %s", msg)
	}
}

func TestCertExportError_NilErr(t *testing.T) {
	err := &CertExportError{Err: nil}
	msg := err.Error()
	if !strings.Contains(msg, "export failed") {
		t.Errorf("expected 'export failed' in error, got: %s", msg)
	}
	if err.Unwrap() != nil {
		t.Error("Unwrap should return nil for nil Err")
	}
}

func TestCertExportError_Unwrap(t *testing.T) {
	inner := io.ErrNoProgress
	err := &CertExportError{Err: inner}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should allow errors.Is to match the inner error")
	}
	var target *CertExportError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *CertExportError")
	}
}

// ---------------------------------------------------------------------------
// PKCS12FIPSUnavailableError
// ---------------------------------------------------------------------------

func TestPKCS12FIPSUnavailableError_Message(t *testing.T) {
	err := &PKCS12FIPSUnavailableError{}
	if !strings.Contains(err.Error(), "fips140=only") {
		t.Errorf("expected 'fips140=only' in error, got: %s", err.Error())
	}
}

func TestPKCS12FIPSUnavailableError_As(t *testing.T) {
	var err error = &PKCS12FIPSUnavailableError{}
	var target *PKCS12FIPSUnavailableError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *PKCS12FIPSUnavailableError")
	}
}

// ---------------------------------------------------------------------------
// CAKeyNotAvailableError
// ---------------------------------------------------------------------------

func TestCAKeyNotAvailableError_Message(t *testing.T) {
	err := &CAKeyNotAvailableError{}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("expected 'not available' in error, got: %s", err.Error())
	}
}

func TestCAKeyNotAvailableError_As(t *testing.T) {
	var err error = &CAKeyNotAvailableError{}
	var target *CAKeyNotAvailableError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *CAKeyNotAvailableError")
	}
}

// ---------------------------------------------------------------------------
// BarrierSealedError
// ---------------------------------------------------------------------------

func TestBarrierSealedError_Message(t *testing.T) {
	err := &BarrierSealedError{}
	if !strings.Contains(err.Error(), "sealed") {
		t.Errorf("expected 'sealed' in error, got: %s", err.Error())
	}
}

func TestBarrierSealedError_As(t *testing.T) {
	var err error = &BarrierSealedError{}
	var target *BarrierSealedError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *BarrierSealedError")
	}
}

// ---------------------------------------------------------------------------
// StoreError
// ---------------------------------------------------------------------------

func TestStoreError_Message(t *testing.T) {
	err := &StoreError{Op: "put", Detail: "__sys:certs/abc", Err: io.EOF}
	msg := err.Error()
	if !strings.Contains(msg, "store put") {
		t.Errorf("expected 'store put' in error, got: %s", msg)
	}
	if !strings.Contains(msg, "__sys:certs/abc") {
		t.Errorf("expected detail in error, got: %s", msg)
	}
	if !strings.Contains(msg, "EOF") {
		t.Errorf("expected cause in error, got: %s", msg)
	}
}

func TestStoreError_NilErr(t *testing.T) {
	err := &StoreError{Op: "get", Detail: "key1"}
	msg := err.Error()
	if !strings.Contains(msg, "get") || !strings.Contains(msg, "key1") {
		t.Errorf("expected op and detail in error, got: %s", msg)
	}
	if err.Unwrap() != nil {
		t.Error("Unwrap should return nil for nil Err")
	}
}

func TestStoreError_EmptyDetail(t *testing.T) {
	err := &StoreError{Op: "scan", Err: io.ErrClosedPipe}
	msg := err.Error()
	if !strings.Contains(msg, "store scan") {
		t.Errorf("expected 'store scan' in error, got: %s", msg)
	}
}

func TestStoreError_Unwrap(t *testing.T) {
	inner := io.ErrShortBuffer
	err := &StoreError{Op: "delete", Detail: "k", Err: inner}
	if !errors.Is(err, inner) {
		t.Error("Unwrap should allow errors.Is to match the inner error")
	}
	var target *StoreError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *StoreError")
	}
	if target.Op != "delete" {
		t.Fatalf("Op: got %q, want %q", target.Op, "delete")
	}
}

// ---------------------------------------------------------------------------
// ValidationError
// ---------------------------------------------------------------------------

func TestValidationError_Message(t *testing.T) {
	err := &ValidationError{Field: "username", Reason: "must not be empty"}
	msg := err.Error()
	if !strings.Contains(msg, "username") {
		t.Errorf("expected field in error, got: %s", msg)
	}
	if !strings.Contains(msg, "must not be empty") {
		t.Errorf("expected reason in error, got: %s", msg)
	}
}

func TestValidationError_As(t *testing.T) {
	var err error = &ValidationError{Field: "ttl", Reason: "must not be negative"}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ValidationError")
	}
	if target.Field != "ttl" {
		t.Fatalf("Field: got %q, want %q", target.Field, "ttl")
	}
	if target.Reason != "must not be negative" {
		t.Fatalf("Reason: got %q, want %q", target.Reason, "must not be negative")
	}
}
