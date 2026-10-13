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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

func TestLegacyDataDirError_Message(t *testing.T) {
	e := &LegacyDataDirError{DataDir: "/var/lib/qrdb", Err: seal.ErrLegacyDataDir}
	msg := e.Error()
	if !strings.Contains(msg, "/var/lib/qrdb") {
		t.Errorf("message %q does not include data dir", msg)
	}
	if !strings.Contains(msg, "pre-encfs") {
		t.Errorf("message %q does not mention pre-encfs", msg)
	}
}

func TestLegacyDataDirError_UnwrapMatchesSealSentinel(t *testing.T) {
	e := &LegacyDataDirError{DataDir: "/x", Err: seal.ErrLegacyDataDir}
	if !errors.Is(e, seal.ErrLegacyDataDir) {
		t.Error("errors.Is should match the wrapped seal sentinel")
	}
}

func TestAdminReplicationTimeoutError(t *testing.T) {
	e := &AdminReplicationTimeoutError{Timeout: 30 * time.Second}
	if !strings.Contains(e.Error(), "30s") || !strings.Contains(e.Error(), "not replicated") {
		t.Errorf("message %q missing timeout / not-replicated", e.Error())
	}
	wrapped := &AuthBootstrapError{Step: "wait for admin replication", Err: e}
	var target *AdminReplicationTimeoutError
	if !errors.As(wrapped, &target) {
		t.Errorf("errors.As should extract AdminReplicationTimeoutError, got %v", wrapped)
	}
}

func TestSealedListenerStartupError_Roundtrip(t *testing.T) {
	root := errors.New("listen tcp :8443: bind: address in use")
	e := &SealedListenerStartupError{Err: root}
	if !strings.Contains(e.Error(), "address in use") {
		t.Errorf("message %q lacks underlying detail", e.Error())
	}
	if !errors.Is(e, root) {
		t.Error("errors.Is failed to unwrap")
	}
}

// TestSystemShardServingError carries the readiness wait's typed error: the
// message names the failure, and errors.As reaches the wait's own error.
func TestSystemShardServingError(t *testing.T) {
	inner := &server.ShardReadWaitError{ShardID: 1, Timeout: time.Second, Polls: 3}
	err := error(&SystemShardServingError{Err: inner})
	if !strings.Contains(err.Error(), "system shard not ready to serve") || !strings.Contains(err.Error(), "shard 1") {
		t.Fatalf("message %q", err.Error())
	}
	var got *server.ShardReadWaitError
	if !errors.As(err, &got) || got != inner {
		t.Fatal("SystemShardServingError does not unwrap to the wait's error")
	}
}
