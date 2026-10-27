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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/storage"

// The closed-store error is a client-facing error: every transport can return
// it, so it stays in the light root while the storage engines themselves are
// re-exported from sdk/go/storage.

// StorageClosedError is returned when storage is closed. It carries no data;
// match ErrStorageClosed rather than this type, so that a backend returning
// the sentinel directly is recognized too.
type StorageClosedError = storage.ClosedError

// ErrStorageClosed is the sentinel every closed-store failure unwraps to,
// whichever backend produced it and however deeply it is wrapped:
//
//	if errors.Is(err, sdk.ErrStorageClosed) {
//		// the node closed the store under this call; conclude, do not crash
//	}
//
// It survives the embedded transport intact — the service layer's ProposeError
// and the transport's RequestError both unwrap. The network transports carry
// the CLASSIFICATION (gRPC Unavailable, HTTP 503) but not the value, because a
// status code is all that crosses the wire; a remote caller uses that instead.
var ErrStorageClosed = storage.ErrStorageClosed

// IsStorageClosed reports whether err is, or wraps, the store having been
// closed. Prefer it to comparing error strings.
var IsStorageClosed = storage.IsClosed
