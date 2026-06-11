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

package seal

import (
	"context"
	"fmt"
	"sync"
	"testing"

	quicraftShamir "github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

// captureDistributor implements CeremonyDistributor for use in unit tests.
// It splits the secret using the real Shamir library, records the share strings
// (so tests can later call UnsealWithShare / RecoverWithKeys), and returns the
// share count. This mirrors what the ceremony package does in production but
// without requiring a full ceremony setup.
type captureDistributor struct {
	mu     sync.Mutex
	shares []string // last round's shares
}

// newCaptureDistributor returns a captureDistributor ready for use.
func newCaptureDistributor() *captureDistributor {
	return &captureDistributor{}
}

// SealAndDistribute splits secret into total shares with the given threshold,
// records them in the distributor, and returns the count.
func (c *captureDistributor) SealAndDistribute(_ context.Context, secret []byte, threshold, total int) (int, error) {
	raw, err := quicraftShamir.Split(secret, threshold, total)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrShamirSplitFailed, err)
	}
	c.mu.Lock()
	c.shares = make([]string, len(raw))
	for i, s := range raw {
		c.shares[i] = s.Value
	}
	c.mu.Unlock()
	return len(raw), nil
}

// RekeyAndDistribute destroys the prior round and produces fresh shares.
func (c *captureDistributor) RekeyAndDistribute(ctx context.Context, newSecret []byte, newThreshold, newTotal int) (int, error) {
	c.mu.Lock()
	c.shares = nil
	c.mu.Unlock()
	return c.SealAndDistribute(ctx, newSecret, newThreshold, newTotal)
}

// Shares returns the share strings captured by the last SealAndDistribute or
// RekeyAndDistribute call. The returned slice is a copy.
func (c *captureDistributor) Shares() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shares == nil {
		return nil
	}
	out := make([]string, len(c.shares))
	copy(out, c.shares)
	return out
}

// combineShareValues reconstructs the split secret from captured share value
// strings. Used by tests that need the distributed secret (the unseal key in
// direct mode) without going through a barrier unseal path.
func combineShareValues(t testing.TB, values []string, threshold, total int) []byte {
	t.Helper()
	shares := make([]*quicraftShamir.Share, len(values))
	for i, v := range values {
		shares[i] = &quicraftShamir.Share{
			Index:     i + 1,
			Threshold: threshold,
			Total:     total,
			Value:     v,
		}
	}
	secret, err := quicraftShamir.Combine(shares)
	if err != nil {
		t.Fatalf("combineShareValues: %v", err)
	}
	return secret
}
