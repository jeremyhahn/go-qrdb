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

//go:build clusterproof

package workload

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// Classifier decides what an operation's error means for the history. It is
// a seam: a run against a transport whose errors say more can pass a sharper
// one, but it must never call a write fail unless the write certainly did not
// apply.
type Classifier func(kind history.Kind, err error) history.Outcome

// definiteWriteRejections are errors a node returns before the write reaches
// the log: the request was refused outright, so it cannot apply later.
var definiteWriteRejections = []func(error) bool{
	isA[*transport.CapabilityUnsupportedError],
	isA[*transport.NotImplementedError],
	isA[*transport.InvalidWriteModeError],
	isA[*transport.ConfigError],
}

// absentOK names the operations for which KeyNotFound is a successful answer.
var absentOK = map[history.Kind]bool{
	history.KindGet:    true,
	history.KindDelete: true,
}

// DefaultClassifier is the conservative classifier.
//
//   - No error is ok, and so is KeyNotFound on a get or a delete.
//   - A read that errs is fail: it changed nothing, so it constrains nothing.
//   - A write that errs is indeterminate -- a timeout, a dropped connection,
//     even NotLeader (the entry may already sit in the old leader's log) --
//     unless the error is a definite rejection that never reached the log.
func DefaultClassifier(kind history.Kind, err error) history.Outcome {
	if err == nil {
		return history.OutcomeOK
	}
	if absentOK[kind] && isA[*transport.KeyNotFoundError](err) {
		return history.OutcomeOK
	}
	if kind == history.KindGet {
		return history.OutcomeFail
	}
	for _, rejected := range definiteWriteRejections {
		if rejected(err) {
			return history.OutcomeFail
		}
	}
	return history.OutcomeIndeterminate
}

// isA reports whether err wraps an error of type T.
func isA[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}

// connectionLost reports whether err means the connection itself is gone, so
// the client should drop it and dial again.
func connectionLost(err error) bool {
	return isA[*transport.ConnectionError](err) || isA[*transport.ClosedError](err)
}
