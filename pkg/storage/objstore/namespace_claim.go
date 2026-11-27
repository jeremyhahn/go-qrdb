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

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
)

// NamespaceClaim is an owner's hold on a provider namespace opened through
// OpenStore rather than New: the store a segment-layout (LSM) engine runs
// on. It applies New's ownership rules to that store, so the namespace is
// refused to any other owner, in this process (*NamespaceInUseError) and
// through its durable owner marker, until Release.
type NamespaceClaim struct {
	e *Engine
}

// ClaimNamespace claims the namespace of store, opened by OpenStore from
// provider and settings, for owner. Another owner holding it open in this
// process, a walked root overlapping an open engine's root, or an owner
// marker naming another owner refuses the claim. The caller ensures the
// store's format marker first: the owner marker is an object in the
// namespace, so a format check that refuses a non-empty namespace without
// a marker must run before it is written.
func ClaimNamespace(provider string, settings map[string]string, store objcommon.Storage, owner string) (*NamespaceClaim, error) {
	if store == nil {
		return nil, &InvalidRemoteError{Reason: "nil store"}
	}
	canonical, err := canonicalNamespace(provider, settings)
	if err != nil {
		return nil, err
	}
	e := &Engine{store: store, provider: provider, namespace: namespaceKey(provider, canonical), owner: owner}
	claimed := false
	if sharesNamespace(provider, canonical) {
		if err := registerNamespace(e); err != nil {
			return nil, err
		}
		defer func() {
			if !claimed {
				unregisterNamespace(e)
			}
		}()
	}
	if rootSetting, walked := walkedRoots[provider]; walked {
		dataRoot := canonical[rootSetting]
		jRoot, err := physicalJournalRoot(dataRoot)
		if err != nil {
			return nil, err
		}
		if err := registerRoots(e, [2]string{dataRoot, jRoot}); err != nil {
			return nil, err
		}
		defer func() {
			if !claimed {
				unregisterRoots(e)
			}
		}()
	}
	if err := e.claimMarker(owner); err != nil {
		return nil, err
	}
	claimed = true
	return &NamespaceClaim{e: e}, nil
}

// claimMarker writes or checks the owner marker under the namespace lock.
// A failed marker PUT that may have committed is rolled back through the
// owner-checked release.
func (e *Engine) claimMarker(owner string) error {
	defer lockNamespace(e.namespace)()
	err := e.claimOwner(owner)
	if err != nil && e.ownerWritten {
		return errors.Join(err, releaseOwnerMarker(e.store, owner))
	}
	return err
}

// Release drops the in-process hold on the namespace. The durable owner
// marker stays: the namespace remains its owner's across restarts. Release
// on a nil claim, or a second Release, does nothing.
func (c *NamespaceClaim) Release() {
	if c == nil || c.e == nil {
		return
	}
	if c.e.namespaceHeld {
		unregisterNamespace(c.e)
	}
	unregisterRoots(c.e)
	c.e = nil
}
