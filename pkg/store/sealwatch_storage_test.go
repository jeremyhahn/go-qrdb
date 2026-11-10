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

package store

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	filestore "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// TestBarrierSealed_StorageRefusals: the storage packages' sealed refusals,
// which cannot carry pkg/seal's sentinels, are sealed refusals too.
func TestBarrierSealed_StorageRefusals(t *testing.T) {
	for _, err := range []error{
		quiesce.ErrSealing,
		&filestore.SealedError{Err: quiesce.ErrSealing},
		&pebblestore.SealedError{Err: pebblestore.ErrBarrierSealed},
		&WriteError{Err: &filestore.SealedError{Err: quiesce.ErrSealing}},
		&WriteError{Err: &pebblestore.SealedError{Err: pebblestore.ErrBarrierSealed}},
	} {
		if !barrierSealed(err) {
			t.Errorf("barrierSealed(%v) = false; want true", err)
		}
	}
}
