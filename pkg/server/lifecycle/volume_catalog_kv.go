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

package lifecycle

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// volumeCatalogKV adapts a service.KVServicer to volume.KVServicer. Every
// method is already shape-identical (Put, PutIfAbsent, CompareAndSwap,
// CompareAndDelete, Scan) and is promoted straight through by the embedded
// interface; only Get is overridden.
//
// Get is overridden because service.KVServicer.Get reports a miss as a
// typed error (*service.KeyNotFoundError, commonly wrapped in
// *service.ReadError) where volume.KVServicer's contract is (nil, nil) --
// see its own doc comment. Without this translation, volume.Catalog's "no
// Assignment/Pool for this id" read returns service's raw not-found error
// instead of (nil, nil), and Catalog methods that specifically check for
// *volume.AssignmentNotFoundError / *volume.PoolNotFoundError to decide
// "absent, proceed" treat it as a hard failure instead.
type volumeCatalogKV struct {
	service.KVServicer
}

// NewVolumeCatalogKVServicer adapts svc to volume.KVServicer, translating a
// miss into (nil, nil) as volume.Catalog requires. Composers (cmd/qrdbd,
// sdk/go/node) use this to build the KVServicer half of volume.NewCatalog's
// dependencies from the SAME service.KVServicer the rest of the node uses,
// rather than passing it directly -- which satisfies volume.KVServicer's
// method set structurally but diverges from its documented Get contract on
// exactly the case Reserve and Resolve depend on.
func NewVolumeCatalogKVServicer(svc service.KVServicer) volume.KVServicer {
	return volumeCatalogKV{KVServicer: svc}
}

// Get implements volume.KVServicer.
func (k volumeCatalogKV) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := k.KVServicer.Get(ctx, key)
	if err != nil {
		var notFound *service.KeyNotFoundError
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, err
	}
	return v, nil
}
