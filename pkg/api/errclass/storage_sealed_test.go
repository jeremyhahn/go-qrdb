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

package errclass

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	filestore "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestClassify_StorageSealedRefusalsAreUnavailable: the storage packages cannot
// import pkg/seal, so a sealing barrier's refusal reaches the API as the file
// store's *SealedError (quiesce.ErrSealing) or Pebble's *SealedError
// (pebble.ErrBarrierSealed). Each is Unavailable, the retryable class, bare or
// under any service wrapper — never the wrapper's own class.
func TestClassify_StorageSealedRefusalsAreUnavailable(t *testing.T) {
	refusals := map[string]error{
		"file store sealing":   &filestore.SealedError{Err: quiesce.ErrSealing},
		"pebble barrier":       &pebblestore.SealedError{Err: pebblestore.ErrBarrierSealed},
		"quiesce gate sealing": quiesce.ErrSealing,
	}
	wrappers := map[string]func(error) error{
		"bare":             func(err error) error { return err },
		"read":             func(err error) error { return &service.ReadError{Err: err} },
		"propose":          func(err error) error { return &service.ProposeError{Err: err} },
		"time series":      func(err error) error { return &service.TimeSeriesError{Op: "write", Err: err} },
		"user store write": func(err error) error { return &user.StoreError{Op: "put", Err: err} },
	}
	for rname, refusal := range refusals {
		for wname, wrap := range wrappers {
			if got := Classify(wrap(refusal)); got != Unavailable {
				t.Errorf("%s / %s: Classify = %v; want %v", rname, wname, got, Unavailable)
			}
		}
	}
}
