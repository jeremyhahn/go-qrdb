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

// Package objstoretest provides an in-memory object-storage provider that
// reads both credential references, as s3 does, and records what each
// Configure was handed. It lets a test outside the objstore seam drive a
// configured credential reference end to end without importing go-objstore
// and without a network.
package objstoretest

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// Delivery is what one Configure of a Provider received: the contents of the
// key and secret the credential references named, empty when none was set.
type Delivery struct {
	Key    string
	Secret string
}

// Provider is a credential-consuming provider registered with the objstore
// seam under a name of its own.
type Provider struct {
	name       string
	mu         sync.Mutex
	deliveries []Delivery
}

var seq atomic.Uint64

// NewProvider registers a fresh Provider under a unique name, failing tb if
// the seam refuses the registration. go-objstore's factory has no
// unregistration, so the name stays served for the life of the test binary.
func NewProvider(tb testing.TB) *Provider {
	tb.Helper()
	p := &Provider{name: "qrdb-objstoretest-" + strconv.FormatUint(seq.Add(1), 10)}
	if err := objstore.RegisterProvider(p.name, objstore.ProviderCredentials{Key: true, Secret: true}, p.open); err != nil {
		tb.Fatalf("objstoretest: register %s: %v", p.name, err)
	}
	return p
}

// Name is the provider name to configure the objstore backend with.
func (p *Provider) Name() string {
	return p.name
}

// Deliveries returns what each Configure of the provider received, in order.
func (p *Provider) Deliveries() []Delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Delivery(nil), p.deliveries...)
}

func (p *Provider) open(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
	var d Delivery
	if creds.Key != nil || creds.Secret != nil {
		if err := creds.Open(func(key, secret []byte) error {
			d = Delivery{Key: string(key), Secret: string(secret)}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	p.deliveries = append(p.deliveries, d)
	p.mu.Unlock()
	store := objmemory.New()
	if err := store.Configure(settings, creds); err != nil {
		return nil, err
	}
	return store, nil
}
