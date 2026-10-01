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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// keyWatchingDistributor is the capture distributor, remembering the domain
// key buffer each distribution was handed.
type keyWatchingDistributor struct {
	*captureDistributor
	handed []*secure.Buffer
}

func (k *keyWatchingDistributor) Distribute(ctx context.Context, secret *secure.Buffer, roster *HolderRoster, digest []byte) (int, error) {
	if secret.Size() != unsealKeySize {
		k.handed = append(k.handed, nil)
	} else {
		k.handed = append(k.handed, secret)
	}
	return k.captureDistributor.Distribute(ctx, secret, roster, digest)
}

// TestRedistribute_DomainKeyLivesInProtectedMemory: the domain key a
// distribution splits is generated into protected memory, handed to the
// distributor as that buffer -- never as an ordinary slice -- and destroyed
// as soon as the distribution returns, so neither side keeps a copy.
func TestRedistribute_DomainKeyLivesInProtectedMemory(t *testing.T) {
	ctx := context.Background()
	f := newHolderFixture(t, 2, 3)
	watch := &keyWatchingDistributor{captureDistributor: f.dist}
	f.b.SetCeremonyDistributor(watch)

	if _, err := f.b.StartRekey(ctx, DomainUnseal, RekeyParams{Threshold: 2}); err != nil {
		t.Fatal(err)
	}
	if o := f.quorum(DomainUnseal, OpRekey, "h1", "h2"); !o.Complete {
		t.Fatalf("rekey incomplete: %+v", o)
	}
	if len(watch.handed) != 1 || watch.handed[0] == nil {
		t.Fatalf("the distributor was not handed a %d-byte protected key: %v", unsealKeySize, watch.handed)
	}
	if size := watch.handed[0].Size(); size != 0 {
		t.Fatalf("the domain key buffer outlived the distribution (size %d)", size)
	}
	// The redistributed shares still open the blob: seal and unseal.
	f.sealWith("h2", "h3")
	if o := f.quorum(DomainUnseal, OpUnseal, "h1", "h3"); !o.Complete || f.b.IsSealed() {
		t.Fatalf("unseal with the new shares: %+v", o)
	}
}
