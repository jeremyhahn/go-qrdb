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

package service

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

func TestLeaseExpiryOptions_DefaultsYieldCheckpointAndGrace(t *testing.T) {
	opts, err := LeaseExpiryOptions(&mockHost{}, 1, config.LeaseConfig{})
	if err != nil {
		t.Fatalf("LeaseExpiryOptions: %v", err)
	}
	if len(opts) != 2 {
		t.Fatalf("options = %d, want 2 (checkpoint and failover grace)", len(opts))
	}
}

func TestLeaseExpiryOptions_ExplicitValues(t *testing.T) {
	cfg := config.LeaseConfig{CheckpointInterval: "250ms", FailoverGrace: "2s"}
	if _, err := LeaseExpiryOptions(&mockHost{}, 1, cfg); err != nil {
		t.Fatalf("LeaseExpiryOptions: %v", err)
	}
}

func TestLeaseExpiryOptions_RejectsUnparsableInterval(t *testing.T) {
	cfg := config.LeaseConfig{CheckpointInterval: "soon"}
	if _, err := LeaseExpiryOptions(&mockHost{}, 1, cfg); err == nil {
		t.Fatal("expected an error for an unparsable checkpoint interval")
	}
}

func TestLeaseExpiryOptions_RejectsUnparsableGrace(t *testing.T) {
	cfg := config.LeaseConfig{FailoverGrace: "later"}
	if _, err := LeaseExpiryOptions(&mockHost{}, 1, cfg); err == nil {
		t.Fatal("expected an error for an unparsable failover grace")
	}
}
