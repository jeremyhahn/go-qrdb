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

package vector

// IndexEffectOp names what a committed vector command does to the
// namespace's in-memory ANN index, as opposed to what it does to the
// replicated store -- the two are applied in different places and at
// different times (docs/vectors/engineering.md "Apply and the index").
type IndexEffectOp byte

const (
	// IndexEffectNone is the zero value: the command committed but has
	// nothing for the in-memory index to do (e.g. a metadata-only update,
	// which never touches a vector's embedding).
	IndexEffectNone IndexEffectOp = iota

	// IndexEffectNamespaceCreate reports a new, empty namespace. The index
	// manager starts an empty live index for it; nothing to rebuild.
	IndexEffectNamespaceCreate

	// IndexEffectNamespaceDelete reports a namespace removed. The index
	// manager discards its live index and any checkpoints on disk.
	IndexEffectNamespaceDelete

	// IndexEffectUpsert reports one vector inserted or replaced. ID and
	// Embedding are populated.
	IndexEffectUpsert

	// IndexEffectDelete reports one vector removed. ID is populated.
	IndexEffectDelete

	// IndexEffectReconfigure reports a namespace's index type, parameters or
	// training generation changed. The index manager discards the live index
	// and marks the namespace for rebuild: an index built under the old
	// geometry cannot be reused under the new one.
	IndexEffectReconfigure
)

// IndexEffect describes one committed vector command's effect on a
// namespace's in-memory ANN index. It carries the namespace record as it
// stood immediately after the command, so the index manager always has the
// current dimension, distance metric, index type, parameters and training
// generation at hand without a separate lookup.
type IndexEffect struct {
	Op        IndexEffectOp
	Namespace string
	ID        uint64
	Embedding []float32
	Record    NamespaceRecord
}
