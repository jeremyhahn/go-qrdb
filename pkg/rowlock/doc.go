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

// Package rowlock is the replicated row-lock table behind SQL's
// SELECT ... FOR UPDATE / FOR SHARE and pessimistic concurrency mode.
//
// The lock table is a set of ordinary records in a database's own store,
// under the internal prefix "__sys:rowlock/", composed from primitives the
// store already has -- no new Raft command:
//
//	lock table = lease (holder liveness) + CAS transaction (enqueue)
//	           + create revision (FIFO order) + watch (wake-up)
//
// Every lock request, granted or waiting, is exactly one record keyed by the
// locked key, the requesting Owner and the mode, and bound to the owner's
// lease. A request's place in its key's queue is the record's create
// revision, which Raft assigns in commit order, so the queue is FIFO and the
// same on every replica. A request is granted when every earlier record on its
// key that belongs to a different owner is compatible with it; because
// records are only appended or deleted, a grant can never be taken back.
//
// The lease belongs to the client session (Session), not to the
// transaction: the owners of a session's successive transactions bind their
// records to one lease the session renews, so a transaction neither grants
// nor revokes one. Releasing an owner deletes its own records; if that
// fails, the session revokes the lease, which deletes every record bound to
// it -- and which is also exactly what lease expiry does when the session's
// process dies.
//
// Deadlocks are detected by the requester, right after it enqueues and before
// it waits, from a linearizable read of the lock table: edges are only ever
// created by an enqueue, so the request that closes a cycle is the newest one
// in it, and it is the victim. An older request on the cycle whose check runs
// late enough to find the cycle too waits for that victim to release: each
// cycle has exactly one victim.
//
// The package locks opaque keys and knows nothing about SQL; pkg/sql/provider
// decides which keys a statement locks. See docs/row-locking/.
package rowlock
