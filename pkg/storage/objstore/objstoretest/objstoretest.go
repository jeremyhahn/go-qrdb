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

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// Delivery is what one Configure of a Provider received: the contents of the
// key and secret the credential references named. Key is empty and Secret nil
// when none was set. Secret is a locked copy owned by the Provider, destroyed
// when the test that created it ends; compare it with Secret.EqualBytes.
type Delivery struct {
	Key    string
	Secret *secure.Buffer
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
// unregistration, so the name stays served for the life of the test binary;
// the secrets it records are destroyed when tb ends.
func NewProvider(tb testing.TB) *Provider {
	tb.Helper()
	p := &Provider{name: "qrdb-objstoretest-" + strconv.FormatUint(seq.Add(1), 10)}
	if err := objstore.RegisterProvider(p.name, objstore.ProviderCredentials{Key: true, Secret: true}, p.open); err != nil {
		tb.Fatalf("objstoretest: register %s: %v", p.name, err)
	}
	tb.Cleanup(p.destroy)
	return p
}

// Name is the provider name to configure the objstore backend with.
func (p *Provider) Name() string {
	return p.name
}

// Deliveries returns what each Configure of the provider received, in order.
// The slice is a copy; the Secret buffers in it are the Provider's own.
func (p *Provider) Deliveries() []Delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Delivery(nil), p.deliveries...)
}

// destroy releases every secret the provider recorded.
func (p *Provider) destroy() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.deliveries {
		d.Secret.Destroy()
	}
}

func (p *Provider) open(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
	var d Delivery
	if creds.Key != nil || creds.Secret != nil {
		if err := creds.Open(func(key, secret []byte) error {
			d.Key = string(key)
			if len(secret) == 0 {
				return nil
			}
			buf, err := secure.NewBufferFromBytes(secret)
			if err != nil {
				return err
			}
			d.Secret = buf
			return nil
		}); err != nil {
			d.Secret.Destroy()
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
