// Copyright 2026 Jeremy Hahn
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

//go:build !integration

package main

import "testing"

// ---------------------------------------------------------------------------
// ConfigBinding.Initialized
// ---------------------------------------------------------------------------

func TestConfigBinding_Initialized_NotInitialized(t *testing.T) {
	// Use a temp config dir so no saved embedded.json is found.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	server := NewEmbeddedServerBinding(log, conn, nil)
	cb := NewConfigBinding(log, server, conn)

	resp := cb.Initialized()
	if resp.Initialized {
		t.Error("expected Initialized=false on fresh ConfigBinding")
	}
}

func TestConfigBinding_Initialized_AfterMarkInitialized(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	server := NewEmbeddedServerBinding(log, conn, nil)
	cb := NewConfigBinding(log, server, conn)

	cb.MarkInitialized()
	resp := cb.Initialized()
	if !resp.Initialized {
		t.Error("expected Initialized=true after MarkInitialized")
	}
}

func TestConfigBinding_Initialized_WhenConnected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	server := NewEmbeddedServerBinding(log, conn, nil)
	cb := NewConfigBinding(log, server, conn)

	resp := cb.Initialized()
	if resp.Initialized {
		t.Error("expected Initialized=false when not connected")
	}
}

// ---------------------------------------------------------------------------
// ConfigBinding.IsEmbeddedAvailable
// ---------------------------------------------------------------------------

func TestConfigBinding_IsEmbeddedAvailable(t *testing.T) {
	log := testLogger(t)
	cb := NewConfigBinding(log, nil, nil)

	// The result depends on the build tag, but it should match
	// the package-level function.
	if cb.IsEmbeddedAvailable() != IsEmbeddedAvailable() {
		t.Error("expected IsEmbeddedAvailable to match package-level function")
	}
}

func TestConfigBinding_IsEmbeddedAvailable_Consistent(t *testing.T) {
	log := testLogger(t)
	cb := NewConfigBinding(log, nil, nil)

	first := cb.IsEmbeddedAvailable()
	second := cb.IsEmbeddedAvailable()
	if first != second {
		t.Errorf("expected consistent IsEmbeddedAvailable results: first=%v second=%v", first, second)
	}
}

// ---------------------------------------------------------------------------
// ConfigBinding nil safety
// ---------------------------------------------------------------------------

func TestConfigBinding_Initialized_NilServer(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	cb := NewConfigBinding(log, nil, conn)

	// Should not panic with nil server.
	resp := cb.Initialized()
	if resp.Initialized {
		t.Error("expected Initialized=false with nil server and no connection")
	}
}

func TestConfigBinding_Initialized_NilConn(t *testing.T) {
	log := testLogger(t)
	cb := NewConfigBinding(log, nil, nil)

	// Should not panic with nil conn and nil server.
	resp := cb.Initialized()
	if resp.Initialized {
		t.Error("expected Initialized=false with nil conn and nil server")
	}
}
