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

//go:build !integration

package main

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// FileWriteError
// ---------------------------------------------------------------------------

func TestFileWriteError_Error(t *testing.T) {
	inner := errors.New("disk full")
	e := &FileWriteError{Path: "/tmp/output.pem", Err: inner}
	expected := "qrdb: failed to write file /tmp/output.pem: disk full"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFileWriteError_Error_DifferentPath(t *testing.T) {
	inner := errors.New("permission denied")
	e := &FileWriteError{Path: "/etc/qrdb/cert.pem", Err: inner}
	expected := "qrdb: failed to write file /etc/qrdb/cert.pem: permission denied"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFileWriteError_Unwrap(t *testing.T) {
	inner := errors.New("disk full")
	e := &FileWriteError{Path: "/tmp/output.pem", Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestFileWriteError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &FileWriteError{Path: "/test", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// DirectoryCreateError
// ---------------------------------------------------------------------------

func TestDirectoryCreateError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	e := &DirectoryCreateError{Path: "/var/lib/qrdb/certs", Err: inner}
	expected := "qrdb: failed to create directory /var/lib/qrdb/certs: permission denied"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDirectoryCreateError_Error_DifferentPath(t *testing.T) {
	inner := errors.New("read-only filesystem")
	e := &DirectoryCreateError{Path: "/mnt/ro/data", Err: inner}
	expected := "qrdb: failed to create directory /mnt/ro/data: read-only filesystem"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDirectoryCreateError_Unwrap(t *testing.T) {
	inner := errors.New("permission denied")
	e := &DirectoryCreateError{Path: "/var/lib/qrdb/certs", Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestDirectoryCreateError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &DirectoryCreateError{Path: "/test", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// InvalidLogLevelError
// ---------------------------------------------------------------------------

func TestInvalidLogLevelError_Error(t *testing.T) {
	e := &InvalidLogLevelError{Level: "verbose"}
	expected := "qrdb: unknown log level: verbose"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestInvalidLogLevelError_Error_DifferentLevel(t *testing.T) {
	e := &InvalidLogLevelError{Level: "fatal"}
	expected := "qrdb: unknown log level: fatal"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestInvalidLogLevelError_ErrorsAs(t *testing.T) {
	var target *InvalidLogLevelError
	e := &InvalidLogLevelError{Level: "test"}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *InvalidLogLevelError")
	}
	if target.Level != "test" {
		t.Fatalf("expected Level %q, got %q", "test", target.Level)
	}
}

// ---------------------------------------------------------------------------
// ErrorsAs type matching for all remaining types
// ---------------------------------------------------------------------------

func TestFileWriteError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *FileWriteError
	e := &FileWriteError{Path: "/x", Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *FileWriteError")
	}
	if target.Path != "/x" {
		t.Fatalf("expected Path %q, got %q", "/x", target.Path)
	}
}

func TestDirectoryCreateError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *DirectoryCreateError
	e := &DirectoryCreateError{Path: "/y", Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *DirectoryCreateError")
	}
	if target.Path != "/y" {
		t.Fatalf("expected Path %q, got %q", "/y", target.Path)
	}
}

// TestCLIErrors_DescribeThemselves covers the messages of the CLI's typed
// refusals that no command test reaches with a full value, and the unwrap
// chain of the database-scope refusal.
func TestCLIErrors_DescribeThemselves(t *testing.T) {
	cause := errors.New("transport says no")
	scoped := &DatabaseScopeError{Database: "orders", Transport: "mcp", Err: cause}
	if !errors.Is(scoped, cause) || scoped.Error() != "qrdb: transport mcp does not support scoping to database orders: transport says no" {
		t.Fatalf("DatabaseScopeError = %q", scoped.Error())
	}
	for want, err := range map[string]error{
		"qrdb: barrier init: --so-pin/--user-pin are not allowed when --barrier-strategy=none":                            &PINsNotAllowedError{Strategy: "none", Op: "init"},
		"qrdb: restore to revision 42 left 3 key(s) unrestored due to conflicts; see output above, or retry with --force": &RestoreConflictsError{Revision: 42, Conflicted: 3},
		`qrdb: invalid --role value "admin": want role_id:tenant_id`:                                                      &InvalidFlagValueError{Flag: "role", Value: "admin", Want: "role_id:tenant_id"},
	} {
		if got := err.Error(); got != want {
			t.Errorf("%T = %q, want %q", err, got, want)
		}
	}
}
