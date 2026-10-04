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

package node

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// systemPendingAdminCertStore confines the ceremony-initialize ->
// first-boot admin-credential hand-off (go-qrdb#548) to the system tenant's
// identity namespace over the node's own raw KV service -- the SAME
// physical keyspace firstboot's own role and user records live in
// (tenant.IdentityNamespace), so the record can never collide with an
// operator's own "pending-admin-cert" key, and so it survives backup/restore
// the same way the rest of the system tenant's identity data does.
type systemPendingAdminCertStore struct{ kv service.KVServicer }

// PendingAdminCertStore adapts kv -- ordinarily n.Services().KV, the node's
// own raw KV service -- into firstboot.PendingAdminCertStore, confined to the
// system tenant's identity namespace, for FirstBootWithCeremony.
func PendingAdminCertStore(kv service.KVServicer) firstboot.PendingAdminCertStore {
	return systemPendingAdminCertStore{kv: kv}
}

func (s systemPendingAdminCertStore) namespacedKey(key string) string {
	return tenant.IdentityNamespace(tenantid.System) + key
}

// PutIfAbsent implements firstboot.PendingAdminCertStore.
func (s systemPendingAdminCertStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return s.kv.PutIfAbsent(ctx, s.namespacedKey(key), value)
}

// Get implements firstboot.PendingAdminCertStore.
func (s systemPendingAdminCertStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.kv.Get(ctx, s.namespacedKey(key))
}

// Put implements firstboot.PendingAdminCertStore.
func (s systemPendingAdminCertStore) Put(ctx context.Context, key string, value []byte) error {
	return s.kv.Put(ctx, s.namespacedKey(key), value)
}

// Delete implements firstboot.PendingAdminCertStore.
func (s systemPendingAdminCertStore) Delete(ctx context.Context, key string) error {
	return s.kv.Delete(ctx, s.namespacedKey(key))
}
