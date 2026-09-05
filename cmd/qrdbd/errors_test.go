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

func TestLeaderElectionTimeoutError(t *testing.T) {
	e := &LeaderElectionTimeoutError{Attempts: 30}
	if !strings.Contains(e.Error(), "30") || !strings.Contains(e.Error(), "no leader") {
		t.Errorf("message %q missing attempt count / no-leader", e.Error())
	}
	// Recoverable as the cause of an AuthBootstrapError via errors.As.
	wrapped := &AuthBootstrapError{Step: "wait for leader", Err: e}
	var target *LeaderElectionTimeoutError
	if !errors.As(wrapped, &target) || target.Attempts != 30 {
		t.Errorf("errors.As should extract LeaderElectionTimeoutError, got %v", wrapped)
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
