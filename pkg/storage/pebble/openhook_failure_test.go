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

package pebble

import (
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

var errAfterOpen = errors.New("after open refused")

// failingAfterOpenHook accepts every configuration and fails AfterOpen.
type failingAfterOpenHook struct{}

func (failingAfterOpenHook) Validate(*Config) error { return nil }
func (failingAfterOpenHook) Apply(*pebble.Options)  {}
func (failingAfterOpenHook) AfterOpen(*pebble.DB, vfs.FS, string) error {
	return errAfterOpen
}

func TestNew_AfterOpenFailureUnregistersSealWatch(t *testing.T) {
	var unregistered []string
	prev := leaseUnregisterHook
	leaseUnregisterHook = func(dir string) { unregistered = append(unregistered, dir) }
	t.Cleanup(func() { leaseUnregisterHook = prev })

	var gate quiesce.Gate
	fs := quiesceFS{FS: vfs.NewMem(), gate: &gate}
	for i := range 3 {
		unregistered = unregistered[:0]
		config := DefaultConfig("db")
		config.FS = fs
		s, err := New(config, WithOpenHook(failingAfterOpenHook{}))
		if s != nil {
			t.Fatalf("attempt %d: New returned a store on a failed open hook", i)
		}
		if !errors.Is(err, errAfterOpen) {
			t.Fatalf("attempt %d: err = %v; want %v", i, err, errAfterOpen)
		}
		var opErr *storage.OpError
		if !errors.As(err, &opErr) || opErr.Op != "open" {
			t.Fatalf("attempt %d: err = %v; want a *storage.OpError for open", i, err)
		}
		// Open registered exactly one lease and succeeded, so the AfterOpen
		// failure must unregister it through SealWatch.Close, once.
		if len(unregistered) != 1 {
			t.Fatalf("attempt %d: %d lease unregisters %v; want 1", i, len(unregistered), unregistered)
		}
	}
}
