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

package objstore

import (
	"errors"
	"strconv"
	"sync"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
)

// ErrInvalidProviderRegistration is returned by RegisterProvider for an
// empty provider name or a nil creator.
var ErrInvalidProviderRegistration = errors.New("objstore: a provider registration needs a name and a creator")

// ProviderCredentials declares which credential references a provider's
// Configure reads.
type ProviderCredentials struct {
	Key    bool
	Secret bool
}

// registeredProvider is one provider added through RegisterProvider: the
// creator that opens it and the credential references it declared it reads.
type registeredProvider struct {
	creator objfactory.StorageCreator
	reads   credentialReads
}

// registeredProviders holds the providers added through RegisterProvider.
// go-objstore's factory keeps its creators in an unsynchronized map, so a
// registration written there races every concurrent registration and open.
// This package never writes that map: go-objstore's init alone fills it,
// before any reader runs, and every provider registered at runtime lives
// here, behind a sync.Map. The pinned providerCredentials table is never
// written either, so go-objstore's own providers keep the reads verified
// against their source.
var registeredProviders sync.Map // string -> registeredProvider

// shippedProviders is every provider name the pinned go-objstore
// (v0.7.0-alpha) ships, as a storage backend or an archiver, whether or not
// its build tag compiled it into this binary. RegisterProvider refuses each
// one: a registration under a shipped name whose tag is off would otherwise
// succeed, and the name would then resolve to a creator other than the one its
// pinned providerCredentials entry describes.
var shippedProviders = map[string]struct{}{
	"s3":           {},
	"azure":        {},
	"gcs":          {},
	"glacier":      {},
	"azurearchive": {},
	"local":        {},
	"memory":       {},
}

// ProviderRegisteredError is returned by RegisterProvider for a name
// go-objstore ships or an earlier registration already holds.
type ProviderRegisteredError struct {
	Provider string
}

func (e *ProviderRegisteredError) Error() string {
	return "objstore: provider " + strconv.Quote(e.Provider) + " is already registered"
}

// RegisterProvider registers creator under name and records which credential
// references it reads, so New delivers a configured reference to it instead
// of refusing the reference as ignored. It is safe for concurrent use, with
// itself and with New. It refuses a name go-objstore ships, as a storage
// backend or an archiver, whether or not its build tag is set, or an earlier
// registration holds: one registration
// never silently replaces another, and a provider go-objstore ships keeps the
// reads pinned for it.
func RegisterProvider(name string, reads ProviderCredentials, creator objfactory.StorageCreator) error {
	if name == "" || creator == nil {
		return ErrInvalidProviderRegistration
	}
	if _, shipped := shippedProviders[name]; shipped || objfactory.IsStorageBackendRegistered(name) || objfactory.IsArchiverRegistered(name) {
		return &ProviderRegisteredError{Provider: name}
	}
	provider := registeredProvider{creator: creator, reads: credentialReads{key: reads.Key, secret: reads.Secret}}
	if _, loaded := registeredProviders.LoadOrStore(name, provider); loaded {
		return &ProviderRegisteredError{Provider: name}
	}
	return nil
}

// resolveProvider returns the provider New opens under name, resolved once so
// the credential declaration it validates and the creator it opens are the
// same registration: a RegisterProvider completing between the two cannot
// slip a creator past the credential check. A provider RegisterProvider added
// opens through its own creator and reads what it declared; one go-objstore
// ships opens through go-objstore's factory and reads its pinned
// providerCredentials entry. It reports false for a name neither knows.
func resolveProvider(name string) (registeredProvider, bool) {
	if stored, ok := registeredProviders.Load(name); ok {
		registered, ok := stored.(registeredProvider)
		return registered, ok
	}
	if !objfactory.IsStorageBackendRegistered(name) {
		return registeredProvider{}, false
	}
	return registeredProvider{
		creator: func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
			return objfactory.NewStorage(name, settings, creds)
		},
		reads: providerCredentials[name],
	}, true
}
