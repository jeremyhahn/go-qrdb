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

package encfs

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// hintlessFS is a filesys.FS whose files carry neither Preallocator nor
// Prefetcher.
type hintlessFS struct{ filesys.FS }

func (h hintlessFS) Create(name string) (filesys.File, error) {
	f, err := h.FS.Create(name)
	if err != nil {
		return nil, err
	}
	return struct{ filesys.File }{f}, nil
}

func TestEncFile_PreallocateWithoutBaseSupportIsNoop(t *testing.T) {
	fs := New(hintlessFS{newBaseFS()}, newFakeBarrier(t))
	f, err := fs.Create("hint.dat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.(filesys.Preallocator).Preallocate(0, 4096); err != nil {
		t.Errorf("Preallocate: %v", err)
	}
}

func TestEncFile_PrefetchWithoutBaseSupportIsNoop(t *testing.T) {
	fs := New(hintlessFS{newBaseFS()}, newFakeBarrier(t))
	f, err := fs.Create("hint.dat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.(filesys.Prefetcher).Prefetch(0, 4096); err != nil {
		t.Errorf("Prefetch: %v", err)
	}
}

func TestNegativePayloadLengthError_Error(t *testing.T) {
	got := (&NegativePayloadLengthError{Length: -3}).Error()
	if got != "encfs: payload length is negative (-3)" {
		t.Errorf("Error() = %q", got)
	}
	if !strings.HasPrefix((&NegativePayloadLengthError{}).Error(), "encfs: ") {
		t.Error("Error() does not name its origin")
	}
}

func TestTrailingBlockTooShortError_Error(t *testing.T) {
	got := (&TrailingBlockTooShortError{Bytes: 7}).Error()
	if got != "encfs: trailing block too short (7 bytes)" {
		t.Errorf("Error() = %q", got)
	}
	if !strings.Contains((&TrailingBlockTooShortError{}).Error(), "(0 bytes)") {
		t.Error("Error() does not carry the byte count")
	}
}

func TestFlushBlockSizeError_Error(t *testing.T) {
	got := (&FlushBlockSizeError{Expected: 4096, Got: 12}).Error()
	if got != "encfs: flushFullBlock expected 4096 bytes, got 12" {
		t.Errorf("Error() = %q", got)
	}
	if !strings.HasPrefix((&FlushBlockSizeError{}).Error(), "encfs: ") {
		t.Error("Error() does not name its origin")
	}
}
