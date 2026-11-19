//go:build desktop

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

package main

import sdk "github.com/jeremyhahn/go-qrdb/sdk/go"

// guiBindings holds every Wails binding instance created for a single
// GUI session. It is the single authoritative source of which bindings
// are registered — both gui_desktop.go and the test suite consult it.
//
// Adding a new Go binding requires three co-ordinated changes:
//  1. Add a field here.
//  2. Initialise it in createGUIBindings.
//  3. Return it from bindList.
//
// Forgetting step 3 produces the bug class: "undefined is not an object
// (evaluating 't.List')" because window.go.main.<Name> is undefined at
// runtime. The test in gui_desktop_bindings_test.go catches this.
type guiBindings struct {
	conn        *ConnectionBinding
	connManager *ConnectionManagerBinding
	server      *EmbeddedServerBinding
	kv          *KVBinding
	admin       *AdminBinding
	mvcc        *MVCCBinding
	tx          *TransactionBinding
	batch       *BatchBinding
	watch       *WatchBinding
	lease       *LeaseBinding
	index       *IndexBinding
	ts          *TimeSeriesBinding
	vector      *VectorBinding
	user        *UserBinding
	invitation  *InvitationBinding
	role        *RoleBinding
	cert        *CertBinding
	db          *DatabaseBinding
	collection  *CollectionBinding
	sql         *SQLBinding
	seal        *SealBinding
	config      *ConfigBinding
	tenant      *TenantBinding
	backup      *BackupBinding
	officer     *OfficerBinding
	ceremony    *CeremonyBinding
	rebalance   *RebalanceBinding
	lock        *LockBinding
	credential  *CredentialBinding
	audit       *AuditBinding
	vlog        *VlogBinding
	daemon      *DaemonBinding
}

// createGUIBindings constructs and wires all Wails bindings for the desktop
// application. The logFile parameter is the path supplied via --log-file;
// pass an empty string when logging to stdout.
func createGUIBindings(log *sdk.ConcreteLogger, logFile string) *guiBindings {
	conn := NewConnectionBinding(log)
	connManager := NewConnectionManagerBinding(log)
	conn.SetManager(connManager)
	sqlBinding := NewSQLBinding(log, conn)
	server := NewEmbeddedServerBinding(log, conn, sqlBinding)

	sealBinding := NewSealBinding(log, server, conn)
	configBinding := NewConfigBinding(log, server, conn)
	sealBinding.SetConfigBinding(configBinding)

	return &guiBindings{
		conn:        conn,
		connManager: connManager,
		server:      server,
		kv:          NewKVBinding(log, conn),
		admin:       NewAdminBinding(log, conn),
		mvcc:        NewMVCCBinding(log, conn),
		tx:          NewTransactionBinding(log, conn),
		batch:       NewBatchBinding(log, conn),
		watch:       NewWatchBinding(log, conn),
		lease:       NewLeaseBinding(log, conn),
		index:       NewIndexBinding(log, conn),
		ts:          NewTimeSeriesBinding(log, conn),
		vector:      NewVectorBinding(log, conn),
		user:        NewUserBinding(log, conn),
		invitation:  NewInvitationBinding(log, conn),
		role:        NewRoleBinding(log, conn),
		cert:        NewCertBinding(log, conn),
		db:          NewDatabaseBinding(log, conn),
		collection:  NewCollectionBinding(log, conn),
		sql:         sqlBinding,
		seal:        sealBinding,
		config:      configBinding,
		tenant:      NewTenantBinding(log, conn),
		backup:      NewBackupBinding(log, conn),
		officer:     NewOfficerBinding(log, conn),
		ceremony:    NewCeremonyBinding(log, conn),
		rebalance:   NewRebalanceBinding(log, conn),
		lock:        NewLockBinding(log, conn),
		credential:  NewCredentialBinding(log, conn),
		audit:       NewAuditBinding(log, conn),
		vlog:        NewVlogBinding(log, conn),
		daemon:      NewDaemonBinding(log, logFile),
	}
}

// bindList returns the slice passed to options.App.Bind in wails.Run.
// The order here determines the order Wails exposes bindings on
// window.go.main; keep it consistent with the field order above.
func (b *guiBindings) bindList() []interface{} {
	return []interface{}{
		b.conn,
		b.connManager,
		b.server,
		b.kv,
		b.admin,
		b.mvcc,
		b.tx,
		b.batch,
		b.watch,
		b.lease,
		b.index,
		b.ts,
		b.vector,
		b.user,
		b.invitation,
		b.role,
		b.cert,
		b.db,
		b.collection,
		b.sql,
		b.seal,
		b.config,
		b.tenant,
		b.backup,
		b.officer,
		b.ceremony,
		b.rebalance,
		b.lock,
		b.credential,
		b.audit,
		b.vlog,
		b.daemon,
	}
}
