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

// BarrierTenantLogNotSupportedError is returned by every LogService call for
// a tenant whose isolation is barrier, on a node without a home group. There
// the log registry is applied on the system shard under the system barrier,
// so the log's entries and its sealed head-signing key would stay readable
// while the tenant's own barrier is sealed. On a node with a home group the
// tenant's logs apply in it, under its barrier, and are not refused.
type BarrierTenantLogNotSupportedError struct {
	TenantID string
}

func (e *BarrierTenantLogNotSupportedError) Error() string {
	return "service: verifiable logs are not available to barrier-isolated tenant " + strconv.Quote(e.TenantID) +
		": the log registry is not protected by the tenant's barrier"
}

// BarrierDatabaseLogNotSupportedError is returned by every LogService call
// naming a database that has its own barrier. The log registry is applied on
// the system shard under the system barrier, so the log's entries and its
// sealed head-signing key would stay readable while the database's barrier is
// sealed. The database is refused before any key is created or log read.
type BarrierDatabaseLogNotSupportedError struct {
	TenantID string
	Database string
}

func (e *BarrierDatabaseLogNotSupportedError) Error() string {
	return "service: verifiable logs are not available in barrier-enabled database " + strconv.Quote(e.Database) +
		" of tenant " + strconv.Quote(e.TenantID) + ": the log registry is not protected by the database's barrier"
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
