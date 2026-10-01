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

package capabilities

import (
	"reflect"
	"testing"
)

// TestSeal_GetReturnsSealedProfileVerbatim pins the runtime-truth
// contract: after a composition root seals the effective profile, Get
// stops synthesizing the legacy compile-time view and returns exactly
// what was sealed.
func TestSeal_GetReturnsSealedProfileVerbatim(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	profile := Capabilities{
		Mode:  ModeStandalone,
		KV:    true,
		Batch: true,
		Index: true,
		// Every raft-only feature deliberately false.
	}
	Seal(profile)

	if !Sealed() {
		t.Fatal("Sealed() = false after Seal")
	}
	got := Get()
	if !reflect.DeepEqual(got, profile) {
		t.Fatalf("Get() = %+v, want the sealed profile %+v", got, profile)
	}
	if got.MVCC || got.Watch || got.Lease || got.Lock || got.Cluster || got.Backup || got.Rebalance || got.Transaction {
		t.Fatal("sealed standalone profile must report every raft-only feature false")
	}
}

// TestGet_UnsealedFallbackIsLegacyRaftView pins the pre-seal fallback:
// library embeddings and tests that never boot a composition root keep
// the historical compile-time behavior — raft mode, core features true,
// build-tagged features per registration.
func TestGet_UnsealedFallbackIsLegacyRaftView(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	if Sealed() {
		t.Fatal("Sealed() = true after Reset")
	}
	got := Get()
	if got.Mode != ModeRaft {
		t.Fatalf("unsealed Mode = %q, want %q", got.Mode, ModeRaft)
	}
	for name, v := range map[string]bool{
		"kv": got.KV, "mvcc": got.MVCC, "transaction": got.Transaction,
		"batch": got.Batch, "watch": got.Watch, "lease": got.Lease,
		"lock": got.Lock, "index": got.Index, "cluster": got.Cluster,
		"backup": got.Backup, "rebalance": got.Rebalance,
	} {
		if !v {
			t.Errorf("unsealed fallback: %s = false, want true", name)
		}
	}
	if got.TimeSeries || got.Vector {
		t.Error("unsealed fallback after Reset must not report unregistered tag features")
	}
}

// TestReset_ClearsSealedProfile verifies Reset returns the package to the
// unsealed state so tests are hermetic.
func TestReset_ClearsSealedProfile(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	Seal(Capabilities{Mode: ModeStandalone})
	Reset()
	if Sealed() {
		t.Fatal("Sealed() = true after Reset")
	}
	if got := Get(); got.Mode != ModeRaft {
		t.Fatalf("post-Reset Get().Mode = %q, want the %q fallback", got.Mode, ModeRaft)
	}
}
