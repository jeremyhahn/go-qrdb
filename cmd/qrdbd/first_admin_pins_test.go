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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// TestFirstAdminPins_NoBarrierInit proves a node without barrier init records
// gives the middleware no pins at all -- a nil interface, not a nil
// *BarrierInit inside one, which the middleware would call and panic on.
func TestFirstAdminPins_NoBarrierInit(t *testing.T) {
	if pins := firstAdminPins(nil); pins != nil {
		t.Fatalf("firstAdminPins(nil) = %#v, want a nil interface", pins)
	}
}

// TestFirstAdminPins_BarrierInit proves a node's barrier init records are
// what the middleware reads the pins from.
func TestFirstAdminPins_BarrierInit(t *testing.T) {
	bi := &lifecycle.BarrierInit{}
	if pins := firstAdminPins(bi); pins != bi {
		t.Fatalf("firstAdminPins(bi) = %#v, want bi", pins)
	}
}
