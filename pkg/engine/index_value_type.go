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

package engine

import (
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// IndexCreateValueType returns the term encoding an index create command
// asks for: numeric when the command carries the numeric flag, string
// otherwise.
//
// Every create handler records it beside the index, because it is the one
// part of a definition a same-owner re-create does not leave to the original.
// A create naming an index that already exists under the same owner is
// otherwise an idempotent no-op; when the value type differs, the handler
// rebuilds the index -- clear, then backfill -- at the requested type. A
// field indexed as a string before it was declared numeric would otherwise
// keep string terms, whose byte order is not numeric order, under a caller
// planning numeric ranges over it. Since the rebuild is a deterministic step
// of the apply path, every replica reaches the same index whether it replays
// the original create or restored past it.
func IndexCreateValueType(opts protocol.IndexCreateOptions) index.IndexValueType {
	if opts.Numeric {
		return index.IndexValueTypeNumeric
	}
	return index.IndexValueTypeString
}
