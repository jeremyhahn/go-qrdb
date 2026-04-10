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

package transport

// CompareTarget selects which field of a key-value is compared in a
// transaction precondition.
type CompareTarget uint8

const (
	// CompareVersion compares the key's version counter.
	CompareVersion CompareTarget = iota

	// CompareCreateRevision compares the key's creation revision.
	CompareCreateRevision

	// CompareModRevision compares the key's modification revision.
	CompareModRevision

	// CompareValue compares the key's value bytes.
	CompareValue
)

// CompareResult specifies the comparison operator for a transaction
// precondition.
type CompareResult uint8

const (
	// CompareEqual requires the field to be equal to the target.
	CompareEqual CompareResult = iota

	// CompareGreater requires the field to be greater than the target.
	CompareGreater

	// CompareLess requires the field to be less than the target.
	CompareLess

	// CompareNotEqual requires the field to not be equal to the target.
	CompareNotEqual
)

// Compare is a single precondition in a transaction.
type Compare struct {
	Key            []byte        `json:"key"`
	Target         CompareTarget `json:"target"`
	Result         CompareResult `json:"result"`
	Value          []byte        `json:"value,omitempty"`
	Version        int64         `json:"version,omitempty"`
	CreateRevision int64         `json:"create_revision,omitempty"`
	ModRevision    int64         `json:"mod_revision,omitempty"`
}

// OpType identifies the type of operation in a transaction or batch.
type OpType uint8

const (
	// OpPut stores a key-value pair.
	OpPut OpType = iota

	// OpGet retrieves a key-value pair.
	OpGet

	// OpDelete removes a key-value pair.
	OpDelete

	// OpRange retrieves a range of key-value pairs.
	OpRange
)

// Op is a single operation within a transaction or batch.
type Op struct {
	Type     OpType `json:"type"`
	Key      []byte `json:"key"`
	Value    []byte `json:"value,omitempty"`
	RangeEnd []byte `json:"range_end,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty"`
}

// OpResponse is the result of a single operation within a transaction
// or batch.
type OpResponse struct {
	Kvs       []*MVCCKeyValue `json:"kvs,omitempty"`
	Count     int64           `json:"count,omitempty"`
	Succeeded bool            `json:"succeeded"`
}

// TransactionRequest is the payload for executing a transaction.
type TransactionRequest struct {
	Compares   []Compare `json:"compares"`
	SuccessOps []Op      `json:"success_ops"`
	FailureOps []Op      `json:"failure_ops"`
}

// TransactionResponse is the result of a transaction.
type TransactionResponse struct {
	Succeeded bool         `json:"succeeded"`
	Revision  int64        `json:"revision"`
	Responses []OpResponse `json:"responses"`
}
