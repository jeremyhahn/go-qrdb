// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package objstore

import (
	"context"
	"io"
	"strings"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
)

// lsmFormatVersion is the object layout of a store whose objects are the
// SSTables of Pebble databases (object-storage-lsm). The flat encoded-name
// layout is formatVersion; neither opens the other's store.
const lsmFormatVersion = "3"

// EnsureLSMFormat refuses a store that was not written by the LSM layout and
// marks an empty one as that layout (object-storage-lsm OS-5 A: refuse at
// open, no migration). Call it once on a store before the first NewRemote
// on it.
//
// The marker is the object formatMarkerName at the store root, the one the
// flat layout checks: a store marked "3" opens; any other value (the flat
// layout writes "2") is a *FormatVersionMismatchError; a non-empty store
// without a marker is a *LSMFormatMarkerMissingError. The marker sits
// outside every Remote prefix, so Remote.List never returns it.
func EnsureLSMFormat(ctx context.Context, store objcommon.Storage) error {
	if store == nil {
		return &InvalidRemoteError{Reason: "nil store"}
	}
	rc, err := store.GetWithContext(ctx, formatMarkerName)
	if err == nil {
		defer func() { _ = rc.Close() }()
		found, readErr := io.ReadAll(rc)
		if readErr != nil {
			return readErr
		}
		if string(found) != lsmFormatVersion {
			return &FormatVersionMismatchError{Found: string(found), Want: lsmFormatVersion}
		}
		return nil
	}
	if !missingIn(ctx, store, formatMarkerName, err) {
		return err
	}
	names, err := store.ListWithContext(ctx, "")
	if err != nil {
		return err
	}
	if len(names) > 0 {
		return &LSMFormatMarkerMissingError{}
	}
	return store.PutWithContext(ctx, formatMarkerName, strings.NewReader(lsmFormatVersion))
}
