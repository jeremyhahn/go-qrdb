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

package protocol

import "encoding/binary"

// CompactionStateKind is what an OpCompactionState command does to its
// scope's record.
type CompactionStateKind byte

const (
	// CompactionStateSetPolicy sets the scope's policy. Body: the policy,
	// JSON (pkg/compaction.Policy), non-empty.
	CompactionStateSetPolicy CompactionStateKind = 1

	// CompactionStateClear removes the scope's record: policy and samples.
	// Body: empty.
	CompactionStateClear CompactionStateKind = 2

	// CompactionStateSample records a revision sample for a period policy.
	// Body: [at:8 LE unix nanoseconds][revision:8 LE].
	CompactionStateSample CompactionStateKind = 3
)

// compactionSampleBodySize is the encoded width of a sample's body.
const compactionSampleBodySize = 16

// CompactionStateCommand is a decoded OpCompactionState command.
type CompactionStateCommand struct {
	// Kind is what the command does.
	Kind CompactionStateKind
	// Scope is the record's scope (pkg/compaction.RecordKey).
	Scope string
	// Policy is a CompactionStateSetPolicy command's policy, JSON.
	Policy []byte
	// At and Revision are a CompactionStateSample command's sample.
	At, Revision int64
}

// compactionStateBodies checks a kind's body when encoding and decoding, and
// is the list of kinds: a kind with no row is unknown.
var compactionStateBodies = map[CompactionStateKind]func(body []byte) bool{
	CompactionStateSetPolicy: func(body []byte) bool { return len(body) > 0 },
	CompactionStateClear:     func(body []byte) bool { return len(body) == 0 },
	CompactionStateSample:    func(body []byte) bool { return len(body) == compactionSampleBodySize },
}

// EncodeCompactionState encodes cmd as an OpCompactionState command:
// [op:1][kind:1][scopeLen:2 LE][scope][body].
func EncodeCompactionState(cmd CompactionStateCommand) ([]byte, error) {
	body := cmd.Policy
	if cmd.Kind == CompactionStateSample {
		body = make([]byte, compactionSampleBodySize)
		binary.LittleEndian.PutUint64(body[0:8], uint64(cmd.At))        //nolint:gosec // G115: two's-complement round trip, decoded back to int64
		binary.LittleEndian.PutUint64(body[8:16], uint64(cmd.Revision)) //nolint:gosec // G115: two's-complement round trip, decoded back to int64
	}
	valid, ok := compactionStateBodies[cmd.Kind]
	if !ok || !valid(body) {
		return nil, &InvalidCommandError{Reason: "compaction state: invalid kind or body"}
	}
	if err := checkUint16Len(len(cmd.Scope), "compaction scope"); err != nil {
		return nil, err
	}
	buf := make([]byte, headerSize+1+2+len(cmd.Scope)+len(body))
	buf[0] = OpCompactionState
	buf[1] = byte(cmd.Kind)
	if err := putUint16Len(buf[2:4], len(cmd.Scope), "compaction scope"); err != nil {
		return nil, err
	}
	copy(buf[4:], cmd.Scope)
	copy(buf[4+len(cmd.Scope):], body)
	return buf, nil
}

// DecodeCompactionState decodes an OpCompactionState payload. A payload with
// an unknown kind, a scope length that overruns it, or a body that does not
// match its kind is refused.
func DecodeCompactionState(payload []byte) (CompactionStateCommand, error) {
	if len(payload) < 3 {
		return CompactionStateCommand{}, &InvalidCommandError{Reason: "compaction state: payload too short"}
	}
	cmd := CompactionStateCommand{Kind: CompactionStateKind(payload[0])}
	scopeLen := int(binary.LittleEndian.Uint16(payload[1:3]))
	if 3+scopeLen > len(payload) {
		return CompactionStateCommand{}, &InvalidCommandError{Reason: "compaction state: scope overruns payload"}
	}
	cmd.Scope = string(payload[3 : 3+scopeLen])
	body := payload[3+scopeLen:]
	valid, ok := compactionStateBodies[cmd.Kind]
	if !ok || !valid(body) {
		return CompactionStateCommand{}, &InvalidCommandError{Reason: "compaction state: invalid kind or body"}
	}
	if cmd.Kind == CompactionStateSample {
		cmd.At = int64(binary.LittleEndian.Uint64(body[0:8]))        //nolint:gosec // G115: two's-complement round trip of the encoded int64
		cmd.Revision = int64(binary.LittleEndian.Uint64(body[8:16])) //nolint:gosec // G115: two's-complement round trip of the encoded int64
		return cmd, nil
	}
	if len(body) > 0 {
		cmd.Policy = append([]byte(nil), body...)
	}
	return cmd, nil
}
