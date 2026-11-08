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

package service

// MaxReplyBytes is the largest encoded reply one node relays to another: the
// gRPC replica forwarder receives no reply larger than this, and a
// transaction whose reply could exceed it is refused before any of its writes
// is staged (TxnReadResultTooLargeError), so a transaction never commits on
// one node and then fails to deliver its result through another. It is a
// constant, not node configuration: a transaction is applied on every
// replica, and every replica must refuse the same transaction at the same
// log position.
const MaxReplyBytes = 4 << 20

// The protobuf encoding of a TransactionResponse is bounded by
// TxnReplyHeaderBytes, plus TxnReplyOpBytes for each op of the branch that
// ran, plus TxnReplyKVBytes and the key and value bytes for each key a read
// answers. Each figure is the largest its fields can encode to: a tag byte
// and a ten-byte varint for every int64, two bytes for a bool, and a tag
// byte and a five-byte length for every embedded message and bytes field.
const (
	// TxnReplyHeaderBytes covers the response's succeeded and revision.
	TxnReplyHeaderBytes = 2 + 11
	// TxnReplyOpBytes covers one OpResponse's framing, count and succeeded.
	TxnReplyOpBytes = 6 + 11 + 2
	// TxnReplyKVBytes covers one KeyValue's framing, its key and value
	// headers, and its four revision, version and lease fields.
	TxnReplyKVBytes = 6 + 6 + 6 + 4*11
)
