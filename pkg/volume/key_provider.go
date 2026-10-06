// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import "context"

// KeyProvider is the seam through which a Lockable volume's per-(node,
// pool) unlock key is obtained. It is shaped like this project's other key
// seams (cakey.Provider, keywrap.Provider): a narrow interface naming no
// key-custody mechanism, so a caller never knows or cares whether the key
// came from a software root or a hardware module.
//
// go-qrdb ships exactly one implementation (the software HKDF derivation
// from the unsealed system barrier's root key, pkg/seal's default). A
// composer with its own key custody (hardware-rooted, a remote KMS, ...)
// injects its own implementation through WithVolumeKeyProvider
// (sdk/go/node, pkg/server/lifecycle) -- go-qrdb never imports that
// custody code, and pkg/seal's VolumeLifecycle never reads a master key
// directly; it only ever calls a KeyProvider.
//
// Implementations must be safe for concurrent use.
type KeyProvider interface {
	// VolumeKey returns the symmetric key that unlocks volumeName on pool
	// for nodeID. Under decision D1 (one volume per pool per node),
	// volumeName is currently always equal to pool, but the parameter is
	// carried separately so a future per-volume extension within a pool
	// does not change this method's signature.
	VolumeKey(ctx context.Context, nodeID, pool, volumeName string) (VolumeKey, error)
}
