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

package lifecycle

import (
	"context"
	"errors"
	"testing"
)

func TestStartNodeWatchers_NothingToStart(t *testing.T) {
	for name, n := range map[string]*Node{"nil node": nil, "bare node": {}} {
		t.Run(name, func(t *testing.T) {
			if err := StartNodeWatchers(context.Background(), n); err != nil {
				t.Fatalf("StartNodeWatchers = %v, want nil", err)
			}
		})
	}
}

// Each watcher is started under the caller's context and is the node's own
// work, and a failure to start one is reported, not swallowed.
func TestStartNodeWatchers_StartsAndReportsEachWatcher(t *testing.T) {
	ctx := context.Background()
	watcher := newCertProviderWithWatch(nil)
	n := &Node{CertProv: watcher}
	if err := StartNodeWatchers(ctx, n); err != nil {
		t.Fatalf("StartNodeWatchers: %v", err)
	}
	if watcher.watchCalls != 1 {
		t.Fatalf("Key B eviction watch started %d times, want once", watcher.watchCalls)
	}
	if err := n.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-watcher.exited:
	default:
		t.Fatal("the Key B eviction watch was still running when Stop returned")
	}

	failing := newCertProviderWithWatch(errors.New("hub unavailable"))
	if err := StartNodeWatchers(ctx, &Node{CertProv: failing}); !errors.Is(err, failing.watchErr) {
		t.Fatalf("StartNodeWatchers = %v, want %v", err, failing.watchErr)
	}
}
