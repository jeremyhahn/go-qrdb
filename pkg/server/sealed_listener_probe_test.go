// Copyright (c) 2026 Jeremy Hahn
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

package server

import (
	"context"
	"testing"
)

type fixedSealedProbe struct{ sealed bool }

func (p fixedSealedProbe) IsSealed() bool { return p.sealed }

// A probe built over a barrier reports its sealed state, and with no ceremony
// core there is no enrollment to keep open.
func TestNewSealedWindowListenerProbe_FollowsTheBarrier(t *testing.T) {
	for _, sealed := range []bool{true, false} {
		p := NewSealedWindowListenerProbe(fixedSealedProbe{sealed: sealed}, nil)
		if p.IsSealed() != sealed {
			t.Errorf("IsSealed() = %v, want %v", p.IsSealed(), sealed)
		}
		if p.EnrollmentOpen(context.Background()) {
			t.Error("enrollment reported open with no ceremony core")
		}
		p.Admit(true)()
		p.Admit(false)()
	}
}

// Without a barrier probe there is nothing to observe, so the probe fails
// closed: unsealed, enrollment closed, and every listener gate refuses.
func TestNewSealedWindowListenerProbe_NilBarrierFailsClosed(t *testing.T) {
	p := NewSealedWindowListenerProbe(nil, nil)
	if p == nil {
		t.Fatal("nil probe returned")
	}
	if p.IsSealed() || p.EnrollmentOpen(context.Background()) {
		t.Fatal("a probe with no barrier admitted a request")
	}
}
