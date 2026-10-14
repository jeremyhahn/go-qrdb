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

//go:build clusterproof && linux

package clockrate

import (
	"testing"
)

// These tests only read: a unit test never changes the host's clock rate.

func TestSystemBootID(t *testing.T) {
	id, err := System{}.BootID()
	if err != nil {
		t.Fatal(err)
	}
	// A boot ID is a UUID, read without its trailing newline.
	if len(id) != 36 || id[8] != '-' {
		t.Fatalf("boot id = %q", id)
	}
	again, err := System{}.BootID()
	if err != nil || again != id {
		t.Fatalf("boot id changed: %q, %v", again, err)
	}
}

func TestSystemReadsRateAndCapability(t *testing.T) {
	r, err := System{}.Rate()
	if err != nil {
		t.Fatal(err)
	}
	if r.Tick < NominalTick*9/10 || r.Tick > NominalTick*11/10 {
		t.Fatalf("tick %d outside the kernel's range", r.Tick)
	}
	if _, err := (System{}).SysTime(); err != nil {
		t.Fatal(err)
	}
	// The harness kernel is refused whatever the capability says.
	id, err := System{}.BootID()
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(System{}, id); err != ErrHarnessKernel {
		t.Fatalf("Check(own kernel) = %v, want ErrHarnessKernel", err)
	}
}
