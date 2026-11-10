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

package node

import (
	"context"
	"errors"
	"testing"
)

// TestStartClusterSeal_NothingToMonitor: a node with no cluster-seal holder,
// host or barriers starts nothing and boots on; a boot whose caller already
// gave up gets its cancellation first, so the failure path runs.
func TestStartClusterSeal_NothingToMonitor(t *testing.T) {
	n := &Node{}
	if err := n.startClusterSeal(context.Background(), nil, nil, nil, nil); err != nil {
		t.Fatalf("startClusterSeal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.startClusterSeal(ctx, nil, nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("startClusterSeal on a canceled boot = %v; want context.Canceled", err)
	}
}
