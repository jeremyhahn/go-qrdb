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

package service

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/feature"
)

// BarrierStoreUnavailableError is returned by every LogService call for a
// barrier tenant on a node whose composer attached no LogGroupSource: the
// barrier-sealed home group the tenant's log lives in cannot be reached here,
// and the log fails closed rather than falling back to the system shard.
type BarrierStoreUnavailableError struct {
	TenantID string
}

func (e *BarrierStoreUnavailableError) Error() string {
	return "service: the barrier-sealed store of barrier tenant " + strconv.Quote(e.TenantID) + " is not reachable on this node"
}

// BarrierDatabaseLogNotSupportedError is returned by every LogService call
// naming a barrier-enabled database, whatever its backend, before any signing
// key is created, any command is proposed or any read is made. A verifiable
// log in such a database has no store under the database's own key yet: the
// tenant's group would hold its entries and signing keys outside that key, and
// they would stay readable while the database is sealed. The log is refused
// until it has one.
type BarrierDatabaseLogNotSupportedError struct {
	TenantID string
	Database string
}

func (e *BarrierDatabaseLogNotSupportedError) Error() string {
	return "service: verifiable logs are not supported in barrier-enabled database " + strconv.Quote(e.Database) +
		" of tenant " + strconv.Quote(e.TenantID) + ": the log has no store under the database's key"
}

// LogFeatureNotActiveError is returned by a LogService write while a cluster
// feature the write's command needs (featuregate.VerifiableLog, and for a
// create or a key rotation featuregate.VerifiableLogKeys) is not yet active on
// every replica of the tenant's group: a replica that cannot apply a command
// must never be sent it.
type LogFeatureNotActiveError struct {
	TenantID string
	ShardID  uint64
	Feature  feature.Feature
}

func (e *LogFeatureNotActiveError) Error() string {
	return "service: verifiable log write not yet active for tenant " + strconv.Quote(e.TenantID) +
		" (shard " + strconv.FormatUint(e.ShardID, 10) + "): the cluster feature gate has not activated " +
		string(e.Feature) + " on every replica of this shard"
}
