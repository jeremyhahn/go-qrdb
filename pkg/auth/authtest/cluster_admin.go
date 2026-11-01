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

package authtest

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Admin is a service.AdminServicer that implements the platform-only methods
// -- AddMember, RemoveMember, PromoteMember, TransferLeadership,
// RequestSnapshot, Drain, the node-keyspace compaction policy's
// NodeCompaction and SetNodeCompaction, and StateHashes -- and counts the
// calls that reached them, so a test can tell a refused request from one
// the service performed. Every other method is delegated to the embedded
// AdminServicer, which a test sets to its package's own admin double when the
// transport calls one at start-up; left nil, reaching one panics, which means
// the test has left the path it meant to drive.
type Admin struct {
	service.AdminServicer
	changes atomic.Int64
}

var (
	_ service.AdminServicer      = (*Admin)(nil)
	_ service.ReplicaStateHasher = (*Admin)(nil)
)

// AddMember records the membership change.
func (a *Admin) AddMember(context.Context, uint64, uint64, string, string) error {
	a.changes.Add(1)
	return nil
}

// PromoteMember records the membership change. Implements
// service.MemberPromoter so Admin stays usable with the REST promote
// handler's type assertion.
func (a *Admin) PromoteMember(context.Context, uint64, uint64) error {
	a.changes.Add(1)
	return nil
}

// TransferLeadership records the membership change. Implements
// service.LeaderTransferrer so Admin stays usable with every transport's
// transfer-leadership handler's type assertion.
func (a *Admin) TransferLeadership(context.Context, uint64, uint64) error {
	a.changes.Add(1)
	return nil
}

// RemoveMember records the membership change.
func (a *Admin) RemoveMember(context.Context, uint64, uint64) error {
	a.changes.Add(1)
	return nil
}

// RequestSnapshot records the snapshot request.
func (a *Admin) RequestSnapshot(context.Context, uint64) error {
	a.changes.Add(1)
	return nil
}

// Drain records the drain.
func (a *Admin) Drain(context.Context) error {
	a.changes.Add(1)
	return nil
}

// NodeCompaction records the read.
func (a *Admin) NodeCompaction(context.Context) (*compaction.Policy, error) {
	a.changes.Add(1)
	return nil, nil
}

// SetNodeCompaction records the policy change.
func (a *Admin) SetNodeCompaction(context.Context, *compaction.Policy) error {
	a.changes.Add(1)
	return nil
}

// StateHashes records the read. Implements service.ReplicaStateHasher so the
// transports' type assertion reaches it.
func (a *Admin) StateHashes(context.Context, []uint64) (*service.StateHashesResponse, error) {
	a.changes.Add(1)
	return &service.StateHashesResponse{}, nil
}

// StartShard counts the shard start.
func (a *Admin) StartShard(context.Context, *service.StartShardRequest) error {
	a.changes.Add(1)
	return nil
}

// ClusterChanges reports how many of the platform-only calls -- add-member,
// remove-member, snapshot, drain, the node compaction policy's read and
// write, and the replica state hashes read -- reached the service.
func (a *Admin) ClusterChanges() int64 { return a.changes.Load() }

// ClusterAdminCaller is one caller of the cluster-membership operations, and
// whether they must admit it.
type ClusterAdminCaller struct {
	Name     string
	Identity *auth.Identity
	Admitted bool
}

// ClusterAdminTenant is the non-system tenant ClusterAdminCallers draws its
// refused callers from.
const ClusterAdminTenant = "acme"

// ClusterAdminCallers returns the callers every transport's cluster-admin
// scope test drives. The built-in Admin and SO roles both grant add_member,
// remove_member, snapshot and drain in every tenant; the cluster is a
// platform asset, so only the system tenant's holders are admitted.
//
// roles passed to the transport must hold the built-in roles of the system
// tenant and of ClusterAdminTenant.
func ClusterAdminCallers(t testing.TB) []ClusterAdminCaller {
	t.Helper()
	return []ClusterAdminCaller{
		{Name: "the system tenant's Admin is served", Identity: AdminIdentity(tenantid.System, "operator"), Admitted: true},
		{Name: "a tenant's Admin is refused", Identity: AdminIdentity(ClusterAdminTenant, "acme-admin")},
		{Name: "a tenant's SO is refused", Identity: SOIdentity(ClusterAdminTenant, "acme-officer")},
	}
}
