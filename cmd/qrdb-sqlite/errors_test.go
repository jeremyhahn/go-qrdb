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

package main

import (
	"errors"
	"fmt"
	"testing"
)

// sentinel is a simple sentinel error used as an underlying cause in tests.
var sentinel = fmt.Errorf("underlying cause")

// ---------------------------------------------------------------------------
// ConfigError
// ---------------------------------------------------------------------------

func TestConfigError_Error(t *testing.T) {
	err := &ConfigError{Field: "mode", Reason: "must be embedded or client"}
	want := "qrdb-sqlite: invalid configuration for mode: must be embedded or client"
	if got := err.Error(); got != want {
		t.Errorf("ConfigError.Error() = %q, want %q", got, want)
	}
}

func TestConfigError_ErrorsAs(t *testing.T) {
	var target *ConfigError
	err := &ConfigError{Field: "consistency", Reason: "unknown level"}
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ConfigError")
	}
	if target.Field != "consistency" {
		t.Errorf("Field = %q, want %q", target.Field, "consistency")
	}
}

// ---------------------------------------------------------------------------
// ConnectionError
// ---------------------------------------------------------------------------

func TestConnectionError_Error(t *testing.T) {
	err := &ConnectionError{Address: "localhost:63003", Err: sentinel}
	want := "qrdb-sqlite: connect to localhost:63003 failed: underlying cause"
	if got := err.Error(); got != want {
		t.Errorf("ConnectionError.Error() = %q, want %q", got, want)
	}
}

func TestConnectionError_Unwrap(t *testing.T) {
	err := &ConnectionError{Address: "localhost:63003", Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find sentinel through ConnectionError.Unwrap()")
	}
}

func TestConnectionError_ErrorsAs(t *testing.T) {
	var target *ConnectionError
	err := &ConnectionError{Address: "unix:///tmp/qrdbd.sock", Err: sentinel}
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ConnectionError")
	}
	if target.Address != "unix:///tmp/qrdbd.sock" {
		t.Errorf("Address = %q, want %q", target.Address, "unix:///tmp/qrdbd.sock")
	}
}

// ---------------------------------------------------------------------------
// TransportError
// ---------------------------------------------------------------------------

func TestTransportError_Error(t *testing.T) {
	err := &TransportError{Transport: "grpc", Err: sentinel}
	want := "qrdb-sqlite: transport grpc failed: underlying cause"
	if got := err.Error(); got != want {
		t.Errorf("TransportError.Error() = %q, want %q", got, want)
	}
}

func TestTransportError_Unwrap(t *testing.T) {
	err := &TransportError{Transport: "rest", Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find sentinel through TransportError.Unwrap()")
	}
}

func TestTransportError_ErrorsAs(t *testing.T) {
	var target *TransportError
	err := &TransportError{Transport: "unix", Err: sentinel}
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *TransportError")
	}
	if target.Transport != "unix" {
		t.Errorf("Transport = %q, want %q", target.Transport, "unix")
	}
}

// ---------------------------------------------------------------------------
// VirtualTableError
// ---------------------------------------------------------------------------

func TestVirtualTableError_Error(t *testing.T) {
	err := &VirtualTableError{Table: "kv", Err: sentinel}
	want := "qrdb-sqlite: virtual table kv registration failed: underlying cause"
	if got := err.Error(); got != want {
		t.Errorf("VirtualTableError.Error() = %q, want %q", got, want)
	}
}

func TestVirtualTableError_Unwrap(t *testing.T) {
	err := &VirtualTableError{Table: "kv", Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find sentinel through VirtualTableError.Unwrap()")
	}
}

func TestVirtualTableError_ErrorsAs(t *testing.T) {
	var target *VirtualTableError
	err := &VirtualTableError{Table: "timeseries", Err: sentinel}
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *VirtualTableError")
	}
	if target.Table != "timeseries" {
		t.Errorf("Table = %q, want %q", target.Table, "timeseries")
	}
}

// ---------------------------------------------------------------------------
// ShellError
// ---------------------------------------------------------------------------

func TestShellError_Error(t *testing.T) {
	err := &ShellError{Err: sentinel}
	want := "qrdb-sqlite: shell error: underlying cause"
	if got := err.Error(); got != want {
		t.Errorf("ShellError.Error() = %q, want %q", got, want)
	}
}

func TestShellError_Unwrap(t *testing.T) {
	err := &ShellError{Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find sentinel through ShellError.Unwrap()")
	}
}

func TestShellError_ErrorsAs(t *testing.T) {
	var target *ShellError
	err := &ShellError{Err: sentinel}
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ShellError")
	}
	if target.Err != sentinel {
		t.Error("ShellError.Err should be the sentinel error")
	}
}

// ---------------------------------------------------------------------------
// TranslationError
// ---------------------------------------------------------------------------

func TestTranslationError_Error(t *testing.T) {
	err := &TranslationError{Query: "SELECT * FROM kv", Err: sentinel}
	want := "qrdb-sqlite: SQL translation failed for query [SELECT * FROM kv]: underlying cause"
	if got := err.Error(); got != want {
		t.Errorf("TranslationError.Error() = %q, want %q", got, want)
	}
}

func TestTranslationError_Unwrap(t *testing.T) {
	err := &TranslationError{Query: "SELECT 1", Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find sentinel through TranslationError.Unwrap()")
	}
}

func TestTranslationError_ErrorsAs(t *testing.T) {
	var target *TranslationError
	err := &TranslationError{Query: "INSERT INTO kv VALUES (?, ?)", Err: sentinel}
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *TranslationError")
	}
	if target.Query != "INSERT INTO kv VALUES (?, ?)" {
		t.Errorf("Query = %q, want %q", target.Query, "INSERT INTO kv VALUES (?, ?)")
	}
}

// ---------------------------------------------------------------------------
// MetadataError
// ---------------------------------------------------------------------------

func TestMetadataError_Error(t *testing.T) {
	err := &MetadataError{Err: sentinel}
	want := "qrdb-sqlite: table metadata discovery failed: underlying cause"
	if got := err.Error(); got != want {
		t.Errorf("MetadataError.Error() = %q, want %q", got, want)
	}
}

func TestMetadataError_Unwrap(t *testing.T) {
	err := &MetadataError{Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is should find sentinel through MetadataError.Unwrap()")
	}
}

func TestMetadataError_ErrorsAs(t *testing.T) {
	var target *MetadataError
	err := &MetadataError{Err: sentinel}
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *MetadataError")
	}
	if target.Err != sentinel {
		t.Error("MetadataError.Err should be the sentinel error")
	}
}
