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

package server

import "github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"

// NewServiceKVStore adapts a KVServicer, with its IndexServicer and
// TransactionServicer, to the KVStore a DAO runs over, so an embedder that
// holds the service layer (rather than raw storage) builds DAOs on it with
// secondary indexes. The transaction servicer is required: without it the
// constructor returns an error rather than a store that cannot guard writes.
//
// It lives here, not in the sdk/go root, because the adapter links the server
// lifecycle package; a caller of the root alone stays lean.
var NewServiceKVStore = lifecycle.NewGuardedKVStoreAdapter
