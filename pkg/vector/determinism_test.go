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

package vector

import (
	"math"
	"testing"
)

func TestDeterministicSeed_SameInputsAlwaysProduceTheSameSeed(t *testing.T) {
	for i := 0; i < 100; i++ {
		a := DeterministicSeed("ns", uint64(i))
		b := DeterministicSeed("ns", uint64(i))
		if a != b {
			t.Fatalf("DeterministicSeed(%q, %d) = %d then %d, want identical", "ns", i, a, b)
		}
	}
}

func TestDeterministicSeed_NeverZero(t *testing.T) {
	// Zero is indistinguishable from an unset ivf.Config.Seed, which ivf
	// treats as "seed from the clock" -- the one value this must never
	// return (go-qrdb vector-replicated review, phase 2).
	for i := 0; i < 1000; i++ {
		if DeterministicSeed("ns", uint64(i)) == 0 {
			t.Fatalf("DeterministicSeed(\"ns\", %d) = 0", i)
		}
	}
}

func TestDeterministicSeed_DiffersAcrossNamespacesAndSalts(t *testing.T) {
	a := DeterministicSeed("ns-a", 0)
	b := DeterministicSeed("ns-b", 0)
	c := DeterministicSeed("ns-a", 1)
	if a == b {
		t.Fatal("DeterministicSeed does not vary with namespace")
	}
	if a == c {
		t.Fatal("DeterministicSeed does not vary with salt")
	}
}

func TestDeterministicUnit_StaysInTheOpenUnitInterval(t *testing.T) {
	for i := 0; i < 10000; i++ {
		u := DeterministicUnit("ns", uint64(i))
		if u <= 0 || u >= 1 {
			t.Fatalf("DeterministicUnit(\"ns\", %d) = %v, want strictly in (0, 1)", i, u)
		}
	}
}

func TestDeterministicUnit_IsDeterministicAndVaries(t *testing.T) {
	u1 := DeterministicUnit("ns", 42)
	u2 := DeterministicUnit("ns", 42)
	if u1 != u2 {
		t.Fatalf("DeterministicUnit(\"ns\", 42) = %v then %v, want identical", u1, u2)
	}
	u3 := DeterministicUnit("ns", 43)
	if u1 == u3 {
		t.Fatal("DeterministicUnit does not vary with id")
	}
}

func TestDeterministicLayer_IsDeterministicAndNonNegative(t *testing.T) {
	const ml = 1.0 / math.Ln2
	seen := make(map[int]bool)
	for i := 0; i < 1000; i++ {
		layer := DeterministicLayer("ns", uint64(i), ml)
		if layer < 0 {
			t.Fatalf("DeterministicLayer(\"ns\", %d, %v) = %d, want >= 0", i, ml, layer)
		}
		again := DeterministicLayer("ns", uint64(i), ml)
		if layer != again {
			t.Fatalf("DeterministicLayer(\"ns\", %d, %v) = %d then %d, want identical", i, ml, layer, again)
		}
		seen[layer] = true
	}
	// Exponential decay should produce more than one layer across 1000 IDs;
	// a constant layer for every ID would mean the formula is not reading
	// DeterministicUnit at all.
	if len(seen) < 2 {
		t.Fatalf("DeterministicLayer produced only %d distinct layer(s) across 1000 IDs, want at least 2", len(seen))
	}
}

func TestDeterministicLayer_DiffersAcrossNamespaces(t *testing.T) {
	const ml = 1.0 / math.Ln2
	differed := false
	for i := 0; i < 50; i++ {
		if DeterministicLayer("ns-a", uint64(i), ml) != DeterministicLayer("ns-b", uint64(i), ml) {
			differed = true
			break
		}
	}
	if !differed {
		t.Fatal("DeterministicLayer does not vary with namespace across 50 IDs")
	}
}
