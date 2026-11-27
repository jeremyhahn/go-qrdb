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
	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// archiveProviders are the go-objstore providers that are archive tiers:
// backup and archive destinations only, whose reads are delayed or billed per
// restore. They cannot hold the SSTables of an LSM store
// (object-storage-lsm), so OpenStore refuses them.
var archiveProviders = map[string]struct{}{
	"glacier":      {},
	"azurearchive": {},
}

// OpenStore resolves provider to a go-objstore store exactly as New does (an
// inline credential setting refused, the provider resolved from the registry,
// credential files read only when the provider declares it reads them) but
// without the flat engine's commit journal or its format marker. The store is
// for the LSM layout (object-storage-lsm): call EnsureLSMFormat on it before
// the first NewRemote.
//
// An empty provider is storage.ErrInvalidPath. An archive-tier provider
// (glacier, azurearchive) is a *ArchiveTierUnsupportedError, refused before
// any setting is checked or credential file read. The credential handles are
// released before OpenStore returns; the caller owns the store.
func OpenStore(provider string, settings map[string]string, opts ...Option) (objcommon.Storage, error) {
	if provider == "" {
		return nil, storage.ErrInvalidPath
	}
	if _, archive := archiveProviders[provider]; archive {
		return nil, &ArchiveTierUnsupportedError{Provider: provider}
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	store, create, creds, release, err := openStore(provider, settings, o)
	defer release()
	if err != nil {
		return nil, err
	}
	// As New does, but with no journal: a store on a symlinked root is
	// reopened on its physical root, else its walk lists nothing.
	store, _, err = canonicalStore(provider, create, settings, store, creds)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// openStore is the store resolution New and OpenStore share: it refuses an
// inline credential setting, resolves the provider once (an unknown one
// refused before any credential file is read), opens the credential files the
// provider declares it reads, and calls the provider's creator. It returns
// the creator and credentials so New can open its journal store with them,
// and release, which the caller must call once the credentials are no longer
// needed; release is never nil.
func openStore(provider string, settings map[string]string, o options) (store objcommon.Storage, creator objfactory.StorageCreator, creds objcommon.Credentials, release func(), err error) {
	release = func() {}
	// A registered creator need not delegate to a go-objstore Configure, so
	// an inline credential is refused here, before any provider sees it.
	if err := RejectCredentialSettings(settings); err != nil {
		return nil, nil, objcommon.Credentials{}, release, err
	}
	// The provider is resolved once, and an unknown one refused before any
	// credential file is read: the declaration checked below and the creator
	// that opens the store are one registration, whatever RegisterProvider
	// does meanwhile.
	resolved, known := resolveProvider(provider)
	providerResolved(provider)
	if !known {
		return nil, nil, objcommon.Credentials{}, release, &UnsupportedProviderError{Provider: provider}
	}
	if err := checkCredentialUse(provider, resolved.reads, o.credentials); err != nil {
		return nil, nil, objcommon.Credentials{}, release, err
	}
	creds, release, err = openCredentials(o.credentials)
	if err != nil {
		return nil, nil, objcommon.Credentials{}, release, err
	}
	creator = resolved.creator
	if o.ownerOnly {
		creator = ownerOnlyCreator(provider, creator)
	}
	store, err = creator(settings, creds)
	if err != nil {
		return nil, nil, objcommon.Credentials{}, release, credentialOpenError(o.credentials, err)
	}
	return store, creator, creds, release, nil
}
