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

// Package sqlitevfs is the page store core of the SQLite VFS design
// (docs/sqlite-vfs/architecture.md): it maps one SQLite database file onto
// the keys sqlite/<db>/meta and sqlite/<db>/page/<pgno>, pins an MVCC
// revision when a transaction begins, serves every read as page@revision,
// buffers dirty pages, and commits them together with the meta record as one
// atomic batch at a single new revision.
//
// The page store is the only journal (no rollback journal, no WAL, no
// shared-memory index): a transaction's writes become visible all at once or
// not at all.
//
// A transaction whose dirty pages exceed the stage threshold commits
// through staging writes under sqlite/<db>/stage/<commit-id>/page/ and one
// publish that moves them into page/ and updates meta at one revision
// (Q2 A). The meta record carries a restore generation: a transaction that
// sees it change fails with ErrSnapshotChanged after a restore (Q4 A).
//
// A Store built WithWriterLock serializes its writers through a distributed
// lock with a lease (Q3 A): Txn.Reserve fails with ErrBusy at once on
// contention, a lost lease fails Commit with ErrBusy, a stale snapshot (the
// meta change counter moved) cannot reserve (ErrSnapshotChanged), and the
// lock holder sweeps the stages a crashed writer left.
//
// A Store built WithOptimistic (Q3 A, the opt-in alternative to the writer
// lock) takes no lock: each transaction records the pages it read, including
// pages read as absent, and Commit checks that read set atomically with its
// write. If any read page changed, Commit fails with ErrSnapshotChanged and
// writes nothing. Writers on disjoint pages both commit. The meta record is
// checked at the commit revision rather than the snapshot revision, since
// every commit rewrites it. A moved page count or page size is a conflict.
// A staged commit's read set travels as the prefix move's compares, which
// the engine checks at apply, atomically with the move; a failed one is
// ErrSnapshotChanged and writes nothing.
//
// The package is unwired. No SQLite engine is linked and no VFS is
// registered. ServiceBackend runs a Store over the server's MVCC and
// transaction services, and NewLockServiceWriter takes its writer lock from
// the server's lock service, but nothing in the server, the SDK or the CLI
// constructs a Store yet. A commit past the tenant's storage quota fails
// with ErrFull and leaves the database whole. Every Backend call takes the
// caller's context, and a transaction reads with the context Begin was
// given. The engine binding, the database count cap and the sqlite-vfs
// entitlement, a page cache and every transport are later slices.
package sqlitevfs
