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

package clusterseal

import (
	"errors"
	"strconv"
)

// ErrInactive is returned by Seal and Clear before the cluster has activated
// the cluster-seal feature. A caller seals or unseals the local barrier only.
var ErrInactive = errors.New("clusterseal: cluster sealing is not active in this cluster")

// InvalidBarrierError reports a barrier name that is none of the three forms.
type InvalidBarrierError struct {
	Name string
}

func (e *InvalidBarrierError) Error() string {
	return "clusterseal: invalid barrier name " + strconv.Quote(e.Name)
}

// CorruptRecordError reports a stored record that cannot be decoded. The
// barrier it names, when it could be read, is refused until it is rewritten.
type CorruptRecordError struct {
	Barrier Barrier
	Err     error
}

func (e *CorruptRecordError) Error() string {
	msg := "clusterseal: corrupt seal record"
	if e.Barrier != "" {
		msg += " for " + strconv.Quote(string(e.Barrier))
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *CorruptRecordError) Unwrap() error { return e.Err }

// SealEpochConflictError reports that a seal or clear lost a race to a
// request of the opposite intent on the same barrier. Read the record and
// retry.
type SealEpochConflictError struct {
	Barrier Barrier
}

func (e *SealEpochConflictError) Error() string {
	return "clusterseal: a concurrent request changed the seal record for " +
		strconv.Quote(string(e.Barrier)) + "; read it and retry"
}

// RecordNotConfirmedError reports that a seal or clear record could not be
// confirmed written: its outcome is unknown. After a seal the receiving node
// is sealed regardless; after a clear the barrier stays sealed.
type RecordNotConfirmedError struct {
	Barrier Barrier
	// Op is "seal" or "clear".
	Op  string
	Err error
}

func (e *RecordNotConfirmedError) Error() string {
	return "clusterseal: " + e.Op + " record for " + strconv.Quote(string(e.Barrier)) +
		" not confirmed: " + e.Err.Error()
}

func (e *RecordNotConfirmedError) Unwrap() error { return e.Err }

// SealedError ends an open stream whose barrier was sealed.
type SealedError struct {
	Barrier Barrier
}

func (e *SealedError) Error() string {
	return "clusterseal: barrier " + strconv.Quote(string(e.Barrier)) + " sealed"
}

// SealStateUnconfirmedError ends an open stream, and refuses service, while
// this node cannot confirm the seal records: its lease ran out or it has not
// read them since it started. It is retryable; the node keeps its keys.
type SealStateUnconfirmedError struct{}

func (e *SealStateUnconfirmedError) Error() string {
	return "clusterseal: this node cannot confirm the cluster seal state; retry"
}

// ConfigError reports an invalid Monitor configuration.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return "clusterseal: invalid config " + e.Field + ": " + e.Reason
}

// AckError reports a node-barrier acknowledgement that could not be written.
// The seal proceeds; the error is logged.
type AckError struct {
	Err error
}

func (e *AckError) Error() string { return "clusterseal: write seal acknowledgement: " + e.Err.Error() }

func (e *AckError) Unwrap() error { return e.Err }
