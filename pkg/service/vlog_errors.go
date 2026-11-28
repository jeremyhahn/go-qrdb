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
	"fmt"
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

// DatabaseLogsExistError is returned by a barrier-enabled database create
// when the name already holds verifiable logs created before the database
// existed: they are plaintext with no incarnation, and a barrier-enabled
// database cannot adopt them. Create the database under another name.
type DatabaseLogsExistError struct {
	TenantID string
	Database string
	Logs     int
}

func (e *DatabaseLogsExistError) Error() string {
	return "service: database " + strconv.Quote(e.Database) + " of tenant " + strconv.Quote(e.TenantID) +
		" already holds " + strconv.Itoa(e.Logs) + " verifiable log(s) created before the database, " +
		"and a barrier-enabled database cannot adopt them"
}

// ReservedKeyError refuses a kv operation on the verifiable log's reserved
// keyspace (vlog.TablePrefix, vlog.LogTablePrefix). A barrier database keeps
// its logs and their signing-key records in its own backend; only the log
// service writes or reads them, never the kv surface. Range is set when a
// delete range overlapped the reserved keyspace rather than naming a key in
// it.
type ReservedKeyError struct {
	Key   string
	Range bool
}

// SQLiteKeyError refuses a plain kv or batch write to a key of a SQLite
// database (sqlite/<db>/...); those keys are written only through the
// SQLite VFS transactions, which admit and count the databases they create.
type SQLiteKeyError struct {
	Key string
}

func (e *SQLiteKeyError) Error() string {
	return fmt.Sprintf("kv: key %q belongs to a SQLite database and is written only through the SQLite VFS", e.Key)
}

func (e *ReservedKeyError) Error() string {
	if e.Range {
		return fmt.Sprintf("kv: delete range overlaps the reserved verifiable-log keyspace %q", e.Key)
	}
	return fmt.Sprintf("kv: key %q is in the reserved verifiable-log keyspace", e.Key)
}
