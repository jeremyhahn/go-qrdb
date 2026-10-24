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

// tenant_stores_helper_test.go holds the per-tenant in-memory store resolver
// shared by the dek and abac policy command tests. It carries no build
// constraint because its callers do not share one: dek_cmd_test.go builds
// under !integration and abac_policy_cmd_test.go under every tag set.
package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// dekCLIStores resolves one in-memory store per tenant.
type dekCLIStores map[string]*kvstore.MemoryKVStore

var (
	_ dek.StoreResolver    = dekCLIStores(nil)
	_ tenant.StoreResolver = dekCLIStores(nil)
)

func (d dekCLIStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	s, ok := d[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		d[tenantID] = s
	}
	return s, nil
}
