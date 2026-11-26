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

package placement

import (
	"context"
	"errors"
	"testing"
)

func TestScheduler_CancelledContextPlacesNothing(t *testing.T) {
	s, err := NewScheduler(&mockTopology{})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := s.Place(ctx, PlaceRequest{TenantID: "t1", Database: "db1", ShardID: 1, RF: 1}); !errors.Is(err, context.Canceled) || p != nil {
		t.Fatalf("Place = (%v, %v), want context.Canceled", p, err)
	}
	existing := &Placement{TenantID: "t1", Database: "db1", ShardID: 1, RF: 1}
	if p, err := s.PlaceAdditional(ctx, existing, 1); !errors.Is(err, context.Canceled) || p != nil {
		t.Fatalf("PlaceAdditional = (%v, %v), want context.Canceled", p, err)
	}
}

func TestScheduler_LoadCeilingOfNoCandidatesIsZero(t *testing.T) {
	s, err := NewScheduler(&mockTopology{})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if got := s.loadCeiling(nil); got != 0 {
		t.Fatalf("loadCeiling(nil) = %v, want 0", got)
	}
}
