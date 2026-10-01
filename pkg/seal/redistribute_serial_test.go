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
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// overlapDistributor is the capture distributor, recording whether a second
// distribution of a domain entered Distribute while the first was inside it.
// The first call starts the second redistribution and waits until every
// goroutine of the test's bubble is durably blocked or done before going on.
type overlapDistributor struct {
	*captureDistributor
	calls      atomic.Int32
	overlapped atomic.Bool
	second     func()
}

func (o *overlapDistributor) Distribute(ctx context.Context, secret *secure.Buffer, roster *HolderRoster, digest []byte) (int, error) {
	if o.calls.Add(1) == 1 {
		go o.second()
		synctest.Wait()
		o.overlapped.Store(o.calls.Load() > 1)
	}
	return o.captureDistributor.Distribute(ctx, secret, roster, digest)
}

// TestRedistribute_SerializedPerDomain: two redistributions of one domain
// never overlap. Interleaved, the second would discard the first's staged
// shares and commit its own, and the first would then persist its blob over
// the second's with no shares in force to open it -- the domain's key lost.
func TestRedistribute_SerializedPerDomain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		f := newHolderFixture(t, 2, 3)
		rootKey, err := secure.NewBufferFromBytes(make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		defer rootKey.Destroy()

		secondDone := make(chan error, 1)
		dist := &overlapDistributor{captureDistributor: f.dist}
		dist.second = func() {
			_, err := f.b.redistribute(ctx, DomainUnseal, rootKey, 2, "", nil, 0)
			secondDone <- err
		}
		f.b.SetCeremonyDistributor(dist)

		if _, err := f.b.redistribute(ctx, DomainUnseal, rootKey, 2, "", nil, 0); err != nil {
			t.Fatalf("first redistribution: %v", err)
		}
		if err := <-secondDone; err != nil {
			t.Fatalf("second redistribution: %v", err)
		}
		if dist.overlapped.Load() {
			t.Fatal("a second redistribution of the domain entered Distribute while the first was inside it")
		}
		if got := dist.calls.Load(); got != 2 {
			t.Fatalf("Distribute calls = %d, want 2", got)
		}
	})
}

// TestRedistribute_WaitHonoursContext: a redistribution waiting for another
// of its domain gives up with the context's error when the context ends.
func TestRedistribute_WaitHonoursContext(t *testing.T) {
	f := newHolderFixture(t, 2, 3)
	release, err := f.b.lockRedistribution(context.Background(), DomainUnseal)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.b.lockRedistribution(ctx, DomainUnseal); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting with a cancelled context = %v, want context.Canceled", err)
	}
	other, err := f.b.lockRedistribution(ctx, DomainRecovery)
	if err != nil {
		t.Fatalf("another domain is not serialized behind this one: %v", err)
	}
	other()
}
