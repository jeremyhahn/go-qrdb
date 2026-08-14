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
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// newBatch creates a batch that buffers writes, serves reads through them, and
// applies them to the object store in order on Commit. Object stores provide no
// native multi-object atomicity, so a mid-commit failure may leave a partial
// result; this matches the file backend's best-effort semantics.
func newBatch(e *Engine) storage.Batch {
	return storage.NewBufferedBatch(e, e.applyBufferedOps)
}

// applyBufferedOps writes a batch's operations to the engine in order.
func (e *Engine) applyBufferedOps(ops []storage.BufferedOp) error {
	for _, op := range ops {
		if op.Deleted {
			if err := e.Delete(op.Key); err != nil {
				return err
			}
			continue
		}
		if err := e.Set(op.Key, op.Value); err != nil {
			return err
		}
	}
	return nil
}
