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
// The package is unwired. No SQLite engine is linked and no VFS is
// registered; nothing in the server, the SDK or the CLI calls it. The engine
// binding, writer serialization, staged commits for oversized transactions,
// quota, a page cache and every transport are later slices.
package sqlitevfs
