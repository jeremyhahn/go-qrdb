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

// Package sqlitevfs builds the SQLite page store's Backend and writer lock
// over a Go SDK client, so a SQLite database opened through the qrdb VFS
// lives in a remote qrdb server reached over gRPC, Unix, QUIC or the
// embedded transport. REST and MCP carry keys and range bounds as text, so
// they cannot carry the binary page keys and are not supported.
//
// NewBackend adapts a transport.MVCCClient and transport.TransactionClient
// to pkg/sqlitevfs.ServiceKV and ServiceTxn and returns the
// sqlitevfs.ServiceBackend over them; NewWriterLock adapts a
// transport.LockClient to the server's writer lock. Every transport.Client,
// and an sdk.Client's MVCC(), Transaction() and Lock(), satisfy them.
//
// The server's write refusals -- a guard that did not hold, a command
// conflict, an exceeded quota, a denied entitlement -- cross every wire as
// structured refusals (a reason and its metadata), and the adapters rebuild
// the server's typed error from them, so Store.Commit and Publish return
// ErrSnapshotChanged, ErrPublishConflict and ErrFull on a remote client as
// they do in process.
//
// The package never imports the SQLite driver itself: the caller registers
// pkg/sqlitevfs/moderncvfs with an Opener over this Backend.
package sqlitevfs
