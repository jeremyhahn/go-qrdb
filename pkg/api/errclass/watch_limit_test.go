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

package errclass

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

func TestClassify_WatcherLimitIsResourceExhausted(t *testing.T) {
	limit := &watch.WatcherLimitError{Max: 2}
	cases := []struct {
		name string
		err  error
	}{
		{"bare", limit},
		{"wrapped", &watch.WatchCreateError{Cause: limit}},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != ResourceExhausted {
			t.Errorf("%s: Classify(%T) = %v, want %v", tc.name, tc.err, got, ResourceExhausted)
		}
	}
}

func TestClassify_WatcherLimitRowClaimsNoOtherWatchError(t *testing.T) {
	for _, err := range []error{watch.ErrCompacted, &watch.HubClosedError{}} {
		if got := Classify(err); got == ResourceExhausted {
			t.Errorf("Classify(%T) = ResourceExhausted; the watcher-limit row must claim only *watch.WatcherLimitError", err)
		}
	}
}
