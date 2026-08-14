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
// SMResolveError
// ---------------------------------------------------------------------------

func TestSMResolveError_Error(t *testing.T) {
	inner := errors.New("registry lookup failed")
	e := &SMResolveError{Name: "mvcc", Err: inner}
	expected := "qrdb: resolve state machine mvcc: registry lookup failed"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestSMResolveError_Error_DifferentName(t *testing.T) {
	inner := errors.New("not registered")
	e := &SMResolveError{Name: "pebble", Err: inner}
	expected := "qrdb: resolve state machine pebble: not registered"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestSMResolveError_Unwrap(t *testing.T) {
	inner := errors.New("registry lookup failed")
	e := &SMResolveError{Name: "mvcc", Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestSMResolveError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &SMResolveError{Name: "test", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// HostCreateError
// ---------------------------------------------------------------------------

func TestHostCreateError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	e := &HostCreateError{Err: inner}
	expected := "qrdb: create host: permission denied"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestHostCreateError_Error_DifferentMessage(t *testing.T) {
	inner := errors.New("config invalid")
	e := &HostCreateError{Err: inner}
	expected := "qrdb: create host: config invalid"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestHostCreateError_Unwrap(t *testing.T) {
	inner := errors.New("permission denied")
	e := &HostCreateError{Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestHostCreateError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &HostCreateError{Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// ShardStartError
// ---------------------------------------------------------------------------

func TestShardStartError_Error(t *testing.T) {
	inner := errors.New("timeout waiting for leader")
	e := &ShardStartError{ShardID: 42, Err: inner}
	expected := "qrdb: start shard: timeout waiting for leader"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestShardStartError_Error_DifferentShard(t *testing.T) {
	inner := errors.New("already started")
	e := &ShardStartError{ShardID: 1, Err: inner}
	expected := "qrdb: start shard: already started"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestShardStartError_Unwrap(t *testing.T) {
	inner := errors.New("timeout waiting for leader")
	e := &ShardStartError{ShardID: 42, Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestShardStartError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &ShardStartError{ShardID: 1, Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// TransportCreateError
// ---------------------------------------------------------------------------

func TestTransportCreateError_Error(t *testing.T) {
	inner := errors.New("connection refused")
	e := &TransportCreateError{Err: inner}
	expected := "qrdb: create transport: connection refused"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestTransportCreateError_Error_DifferentMessage(t *testing.T) {
	inner := errors.New("invalid config")
	e := &TransportCreateError{Err: inner}
	expected := "qrdb: create transport: invalid config"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestTransportCreateError_Unwrap(t *testing.T) {
	inner := errors.New("connection refused")
	e := &TransportCreateError{Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestTransportCreateError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &TransportCreateError{Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// ClientCreateError
// ---------------------------------------------------------------------------

func TestClientCreateError_Error(t *testing.T) {
	inner := errors.New("invalid credentials")
	e := &ClientCreateError{Err: inner}
	expected := "qrdb: create client: invalid credentials"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestClientCreateError_Error_DifferentMessage(t *testing.T) {
	inner := errors.New("timeout")
	e := &ClientCreateError{Err: inner}
	expected := "qrdb: create client: timeout"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestClientCreateError_Unwrap(t *testing.T) {
	inner := errors.New("invalid credentials")
	e := &ClientCreateError{Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestClientCreateError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &ClientCreateError{Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// DataDirCreateError
// ---------------------------------------------------------------------------

func TestDataDirCreateError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	e := &DataDirCreateError{Path: "/var/lib/qrdb", Err: inner}
	expected := "qrdb: create data directory /var/lib/qrdb: permission denied"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDataDirCreateError_Error_DifferentPath(t *testing.T) {
	inner := errors.New("disk full")
	e := &DataDirCreateError{Path: "/tmp/data", Err: inner}
	expected := "qrdb: create data directory /tmp/data: disk full"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestDataDirCreateError_Unwrap(t *testing.T) {
	inner := errors.New("permission denied")
	e := &DataDirCreateError{Path: "/var/lib/qrdb", Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestDataDirCreateError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &DataDirCreateError{Path: "/test", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

// ---------------------------------------------------------------------------
// ErrorsAs type matching
// ---------------------------------------------------------------------------

func TestSMResolveError_ErrorsAs(t *testing.T) {
	inner := errors.New("not found")
	var target *SMResolveError
	e := &SMResolveError{Name: "test", Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *SMResolveError")
	}
	if target.Name != "test" {
		t.Fatalf("expected Name %q, got %q", "test", target.Name)
	}
}

func TestHostCreateError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *HostCreateError
	e := &HostCreateError{Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *HostCreateError")
	}
}

func TestShardStartError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *ShardStartError
	e := &ShardStartError{ShardID: 7, Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *ShardStartError")
	}
	if target.ShardID != 7 {
		t.Fatalf("expected ShardID 7, got %d", target.ShardID)
	}
}

func TestTransportCreateError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *TransportCreateError
	e := &TransportCreateError{Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *TransportCreateError")
	}
}

func TestClientCreateError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *ClientCreateError
	e := &ClientCreateError{Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *ClientCreateError")
	}
}

func TestDataDirCreateError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *DataDirCreateError
	e := &DataDirCreateError{Path: "/x", Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *DataDirCreateError")
	}
	if target.Path != "/x" {
		t.Fatalf("expected Path %q, got %q", "/x", target.Path)
	}
}

// ---------------------------------------------------------------------------
// ConfigSaveError
// ---------------------------------------------------------------------------

func TestConfigSaveError_Error(t *testing.T) {
	inner := errors.New("disk full")
	e := &ConfigSaveError{Path: "/tmp/embedded.json", Err: inner}
	expected := "qrdb: save embedded config /tmp/embedded.json: disk full"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestConfigSaveError_Error_DifferentPath(t *testing.T) {
	inner := errors.New("permission denied")
	e := &ConfigSaveError{Path: "/etc/qrdb/config.json", Err: inner}
	expected := "qrdb: save embedded config /etc/qrdb/config.json: permission denied"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestConfigSaveError_Unwrap(t *testing.T) {
	inner := errors.New("disk full")
	e := &ConfigSaveError{Path: "/tmp/embedded.json", Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestConfigSaveError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &ConfigSaveError{Path: "/test", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

func TestConfigSaveError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *ConfigSaveError
	e := &ConfigSaveError{Path: "/x", Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *ConfigSaveError")
	}
	if target.Path != "/x" {
		t.Fatalf("expected Path %q, got %q", "/x", target.Path)
	}
}

// ---------------------------------------------------------------------------
// ConfigLoadError
// ---------------------------------------------------------------------------

func TestConfigLoadError_Error(t *testing.T) {
	inner := errors.New("corrupt JSON")
	e := &ConfigLoadError{Path: "/tmp/embedded.json", Err: inner}
	expected := "qrdb: load embedded config /tmp/embedded.json: corrupt JSON"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestConfigLoadError_Error_DifferentPath(t *testing.T) {
	inner := errors.New("file not found")
	e := &ConfigLoadError{Path: "/etc/qrdb/config.json", Err: inner}
	expected := "qrdb: load embedded config /etc/qrdb/config.json: file not found"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestConfigLoadError_Unwrap(t *testing.T) {
	inner := errors.New("corrupt JSON")
	e := &ConfigLoadError{Path: "/tmp/embedded.json", Err: inner}
	if unwrapped := e.Unwrap(); unwrapped != inner {
		t.Fatalf("expected unwrapped error %v, got %v", inner, unwrapped)
	}
}

func TestConfigLoadError_Unwrap_ErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &ConfigLoadError{Path: "/test", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Fatal("expected errors.Is to match sentinel via Unwrap")
	}
}

func TestConfigLoadError_ErrorsAs(t *testing.T) {
	inner := errors.New("fail")
	var target *ConfigLoadError
	e := &ConfigLoadError{Path: "/x", Err: inner}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *ConfigLoadError")
	}
	if target.Path != "/x" {
		t.Fatalf("expected Path %q, got %q", "/x", target.Path)
	}
}

// ---------------------------------------------------------------------------
// NoSavedConfigError
// ---------------------------------------------------------------------------

func TestNoSavedConfigError_Error(t *testing.T) {
	e := &NoSavedConfigError{}
	expected := "qrdb: no saved embedded configuration found"
	if got := e.Error(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestNoSavedConfigError_ErrorsAs(t *testing.T) {
	var target *NoSavedConfigError
	e := &NoSavedConfigError{}
	if !errors.As(e, &target) {
		t.Fatal("expected errors.As to match *NoSavedConfigError")
	}
}
