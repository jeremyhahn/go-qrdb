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

package provider

import (
	"strings"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// ConcurrencyVariable is the session system variable selecting the SQL
// concurrency mode of the session's transactions:
//
//	SET qrdb_concurrency = 'pessimistic';  -- DML takes row locks
//	SET qrdb_concurrency = 'optimistic';   -- DML relies on the commit CAS
//	SET qrdb_concurrency = 'database';     -- the default: the database's own sql.concurrency
//	SELECT @@qrdb_concurrency;
//
// It is SESSION-scoped only, for the same reason as qrdb_isolation: a global
// value would silently change the behaviour of every other connection. It is
// sampled once per transaction. SELECT ... FOR UPDATE and FOR SHARE lock in
// every mode. See docs/row-locking/.
const ConcurrencyVariable = "qrdb_concurrency"

// ConcurrencyDatabase is the qrdb_concurrency value that defers to the
// database's configured mode, and the variable's default.
const ConcurrencyDatabase = "database"

// LockWaitTimeoutVariable is MySQL's innodb_lock_wait_timeout: how many
// seconds a row-lock request waits before failing with MySQL 1205. The
// PostgreSQL wire translates lock_timeout onto it.
const LockWaitTimeoutVariable = "innodb_lock_wait_timeout"

// Lock-wait timeout bounds, MySQL's own: the default is 50 seconds and the
// range 1 to 1073741824.
const (
	DefaultLockWaitTimeoutSeconds int64 = 50
	MaxLockWaitTimeoutSeconds     int64 = 1073741824
)

// concurrencyValues is the order qrdb_concurrency's enum values are
// registered in.
var concurrencyValues = []string{
	ConcurrencyDatabase,
	string(database.ConcurrencyOptimistic),
	string(database.ConcurrencyPessimistic),
}

// init registers qrdb_concurrency, and re-registers innodb_lock_wait_timeout
// with MySQL's range and default: go-mysql-server declares it with the range
// [1, 1] and default 1, which makes SET innodb_lock_wait_timeout = 50 -- the
// statement every MySQL client library knows -- an out-of-range error.
func init() {
	sqle.SystemVariables.AddSystemVariables([]sqle.SystemVariable{
		&sqle.MysqlSystemVariable{
			Name:              ConcurrencyVariable,
			Scope:             sqle.GetMysqlScope(sqle.SystemVariableScope_Session),
			Dynamic:           true,
			SetVarHintApplies: false,
			Type:              types.NewSystemEnumType(ConcurrencyVariable, concurrencyValues...),
			Default:           ConcurrencyDatabase,
		},
		&sqle.MysqlSystemVariable{
			Name:              LockWaitTimeoutVariable,
			Scope:             sqle.GetMysqlScope(sqle.SystemVariableScope_Both),
			Dynamic:           true,
			SetVarHintApplies: true,
			Type:              types.NewSystemIntType(LockWaitTimeoutVariable, 1, MaxLockWaitTimeoutSeconds, false),
			Default:           DefaultLockWaitTimeoutSeconds,
		},
	})
}

// concurrencyChoice returns the session's qrdb_concurrency value, the
// default ("database") when the session does not carry the variable.
func (s *QRDBSession) concurrencyChoice(ctx *sqle.Context) string {
	value, err := s.GetSessionVariable(ctx, ConcurrencyVariable)
	if err != nil {
		return ConcurrencyDatabase
	}
	name, ok := value.(string)
	if !ok {
		return ConcurrencyDatabase
	}
	return strings.ToLower(name)
}

// lockWaitTimeout returns the session's innodb_lock_wait_timeout, the
// default when the session does not carry the variable.
func (s *QRDBSession) lockWaitTimeout(ctx *sqle.Context) time.Duration {
	value, err := s.GetSessionVariable(ctx, LockWaitTimeoutVariable)
	if err != nil {
		return time.Duration(DefaultLockWaitTimeoutSeconds) * time.Second
	}
	seconds, ok := toInt64(value)
	if !ok || seconds < 1 {
		return time.Duration(DefaultLockWaitTimeoutSeconds) * time.Second
	}
	return time.Duration(min(seconds, MaxLockWaitTimeoutSeconds)) * time.Second
}

// toInt64 reads an integer system-variable value, whichever integer width
// the engine stored it as.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case uint64:
		return int64(min(n, uint64(MaxLockWaitTimeoutSeconds))), true
	case uint32:
		return int64(n), true
	}
	return 0, false
}

// effectiveConcurrency resolves a transaction's sampled qrdb_concurrency
// choice against the database's configured mode.
func effectiveConcurrency(choice string, dbDefault database.Concurrency) database.Concurrency {
	if mode, ok := sessionOverrides[choice]; ok {
		return mode
	}
	return (&database.SQLConfig{Concurrency: dbDefault}).EffectiveConcurrency()
}

// sessionOverrides are the qrdb_concurrency values that override the
// database's mode; any other value ("database") defers to it.
var sessionOverrides = map[string]database.Concurrency{
	string(database.ConcurrencyOptimistic):  database.ConcurrencyOptimistic,
	string(database.ConcurrencyPessimistic): database.ConcurrencyPessimistic,
}
