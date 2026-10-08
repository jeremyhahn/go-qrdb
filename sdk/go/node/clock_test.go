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

package node

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

func TestWithClock_SetsTheNodeClock(t *testing.T) {
	fake := clocktest.New()
	o := &nodeOptions{}
	WithClock(fake)(o)
	if o.clock != fake {
		t.Fatalf("WithClock did not set the clock: got %v", o.clock)
	}
}

func TestWithClock_NilLeavesTheRealClock(t *testing.T) {
	o := &nodeOptions{}
	WithClock(nil)(o)
	if o.clock != nil {
		t.Fatalf("a nil clock must leave the default, got %v", o.clock)
	}
}
