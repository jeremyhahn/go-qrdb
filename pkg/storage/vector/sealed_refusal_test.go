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

package vector

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	filestore "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// TestBarrierSealed_RecognisesEverySealedRefusal: the vector adapter treats the
// storage packages' sealed refusals exactly like pkg/seal's own sentinels.
func TestBarrierSealed_RecognisesEverySealedRefusal(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{seal.ErrSealed, true},
		{seal.ErrBarrierSealed, true},
		{quiesce.ErrSealing, true},
		{&filestore.SealedError{Err: quiesce.ErrSealing}, true},
		{&pebblestore.SealedError{Err: pebblestore.ErrBarrierSealed}, true},
		{fmt.Errorf("put: %w", &filestore.SealedError{Err: quiesce.ErrSealing}), true},
		{fmt.Errorf("put: %w", &pebblestore.SealedError{Err: pebblestore.ErrBarrierSealed}), true},
		{errors.New("disk full"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := barrierSealed(tc.err); got != tc.want {
			t.Errorf("barrierSealed(%v) = %v; want %v", tc.err, got, tc.want)
		}
	}
}
