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

package database

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/logdb"
)

// protectedFake is a shared WAL that declares its at-rest protection.
type protectedFake struct {
	*plainFake
	protection logdb.AtRestProtection
}

func (p *protectedFake) AtRestProtection() logdb.AtRestProtection { return p.protection }

// TestMultiplexLogDB_AtRestProtection_DelegatesToTheSharedWAL: dedicated
// routes are always barrier-encrypted, so the multiplex is protected exactly
// when its shared route is.
func TestMultiplexLogDB_AtRestProtection_DelegatesToTheSharedWAL(t *testing.T) {
	for _, want := range []logdb.AtRestProtection{logdb.AtRestUnprotected, logdb.AtRestNonDurable, logdb.AtRestBarrierEncrypted} {
		m, err := NewMultiplexLogDB(&protectedFake{plainFake: &plainFake{}, protection: want}, nil)
		if err != nil {
			t.Fatalf("NewMultiplexLogDB: %v", err)
		}
		if got := m.AtRestProtection(); got != want {
			t.Fatalf("AtRestProtection = %v, want the shared WAL's %v", got, want)
		}
	}
}

// TestMultiplexLogDB_AtRestProtection_UndeclaredFailsClosed: a shared WAL
// that says nothing about its protection is read as unprotected, never safe.
func TestMultiplexLogDB_AtRestProtection_UndeclaredFailsClosed(t *testing.T) {
	m, err := NewMultiplexLogDB(&plainFake{}, nil)
	if err != nil {
		t.Fatalf("NewMultiplexLogDB: %v", err)
	}
	if got := m.AtRestProtection(); got != logdb.AtRestUnprotected {
		t.Fatalf("AtRestProtection over an undeclared WAL = %v, want %v", got, logdb.AtRestUnprotected)
	}
}

func TestRaftWALFormatError_NamesBothFormatsAndTheRemedy(t *testing.T) {
	err := &RaftWALFormatError{Dir: "/wal", Found: WALFormat("plaintext"), Want: WALFormat("barrier")}
	msg := err.Error()
	for _, want := range []string{"/wal", "plaintext", "barrier", "empty WAL directory"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q does not mention %q", msg, want)
		}
	}
	unmarked := (&RaftWALFormatError{Dir: "/wal", Want: WALFormat("barrier")}).Error()
	if !strings.Contains(unmarked, "unknown (unmarked)") {
		t.Fatalf("an unmarked WAL is not named as such: %q", unmarked)
	}
}

func TestRaftWALMarkerError_WrapsTheCause(t *testing.T) {
	cause := errors.New("disk full")
	err := &RaftWALMarkerError{Path: "/wal/FORMAT", Err: cause}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "/wal/FORMAT") {
		t.Fatalf("RaftWALMarkerError = %v", err)
	}
}

func TestIncarnationError_WrapsTheRandomSource(t *testing.T) {
	cause := errors.New("entropy unavailable")
	err := &IncarnationError{Err: cause}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "incarnation") {
		t.Fatalf("IncarnationError = %v", err)
	}
}

func TestShardIDClaimError_WrapsTheClaim(t *testing.T) {
	cause := errors.New("store unavailable")
	err := &ShardIDClaimError{Err: cause}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "shard ID") {
		t.Fatalf("ShardIDClaimError = %v", err)
	}
}
