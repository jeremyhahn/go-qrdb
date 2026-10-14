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

package indexkey

import (
	"bytes"
	"testing"
)

func TestSuccessor_NormalByte(t *testing.T) {
	got := Successor([]byte{0x00, 0x01})
	if !bytes.Equal(got, []byte{0x00, 0x02}) {
		t.Errorf("Successor: got %v, want [0x00, 0x02]", got)
	}
}

func TestSuccessor_AllFF(t *testing.T) {
	// When all bytes are 0xFF, a 0x00 byte is appended.
	got := Successor([]byte{0xFF, 0xFF})
	if !bytes.Equal(got, []byte{0xFF, 0xFF, 0x00}) {
		t.Errorf("Successor(all 0xFF): got %v, want [0xFF, 0xFF, 0x00]", got)
	}
}

func TestSuccessor_SingleByte(t *testing.T) {
	got := Successor([]byte{0x41})
	if !bytes.Equal(got, []byte{0x42}) {
		t.Errorf("Successor: got %v, want [0x42]", got)
	}
}

func TestSuccessor_CarryOver(t *testing.T) {
	// 0xFF increments carry to produce [0x02].
	got := Successor([]byte{0x01, 0xFF})
	if !bytes.Equal(got, []byte{0x02}) {
		t.Errorf("Successor(carry): got %v, want [0x02]", got)
	}
}

func TestSuccessor_DoesNotMutateInput(t *testing.T) {
	in := []byte{0x00, 0x01}
	_ = Successor(in)
	if !bytes.Equal(in, []byte{0x00, 0x01}) {
		t.Errorf("Successor mutated its input: %v", in)
	}
}

func TestSuccessor_Empty(t *testing.T) {
	if got := Successor(nil); !bytes.Equal(got, []byte{0x00}) {
		t.Errorf("Successor(nil): got %v, want [0x00]", got)
	}
}
