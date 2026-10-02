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

package auth

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// soKnownGoodPrefix is the KV key prefix for each tenant's SO PIN known-good
// principals (see SOPINLockout). Full key: __sys:so-pin-known-good/{tenantID}.
const soKnownGoodPrefix = "__sys:so-pin-known-good/"

// soKnownGoodStore persists the tenant SO PIN lockout's known-good sets in
// the authenticator's KV store, beside the SO PIN commitments, so a Security
// Officer keeps its exemption from the aggregate cap across a restart.
type soKnownGoodStore struct {
	store kvstore.KVStore
}

var _ SOPINKnownGoodStore = soKnownGoodStore{}

// LoadKnownGood reads tenantID's known-good principals; none is an empty set.
func (s soKnownGoodStore) LoadKnownGood(tenantID string) ([]string, error) {
	data, err := s.store.Get(context.Background(), soKnownGoodPrefix+tenantID)
	if err != nil {
		return nil, &OperationError{Op: "known-good get", Cause: err}
	}
	if len(data) == 0 {
		return nil, nil
	}
	var principals []string
	if err := json.Unmarshal(data, &principals); err != nil {
		return nil, &OperationError{Op: "known-good decode", Cause: err}
	}
	return principals, nil
}

// SaveKnownGood replaces tenantID's known-good principals.
func (s soKnownGoodStore) SaveKnownGood(tenantID string, principals []string) error {
	data, err := json.Marshal(principals)
	if err != nil {
		return &OperationError{Op: "known-good encode", Cause: err}
	}
	if err := s.store.Put(context.Background(), soKnownGoodPrefix+tenantID, data); err != nil {
		return &OperationError{Op: "known-good put", Cause: err}
	}
	return nil
}
