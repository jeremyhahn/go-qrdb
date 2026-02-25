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

// Package kv provides the foundational KeyValue type used across the QRDB
// codebase. This package has zero external dependencies, serving as the
// base type that breaks import cycles between feature packages.
package kv

// KeyValue is a versioned key-value pair with MVCC revision metadata.
type KeyValue struct {
	// Key is the user-provided key in bytes.
	Key []byte

	// Value is the user-provided value in bytes.
	Value []byte

	// CreateRevision is the global revision at which this key was created.
	CreateRevision int64

	// ModRevision is the global revision of the last modification.
	ModRevision int64

	// Version is the number of modifications since creation. A delete
	// resets the version to zero.
	Version int64

	// LeaseID is the lease attached to this key. Zero means no lease.
	LeaseID int64
}

// Clone creates a deep copy of the KeyValue.
func (kv *KeyValue) Clone() *KeyValue {
	if kv == nil {
		return nil
	}

	clone := &KeyValue{
		CreateRevision: kv.CreateRevision,
		ModRevision:    kv.ModRevision,
		Version:        kv.Version,
		LeaseID:        kv.LeaseID,
	}

	if len(kv.Key) > 0 {
		clone.Key = make([]byte, len(kv.Key))
		copy(clone.Key, kv.Key)
	}

	if len(kv.Value) > 0 {
		clone.Value = make([]byte, len(kv.Value))
		copy(clone.Value, kv.Value)
	}

	return clone
}
