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

package statemachine

// Test helpers shared across this package's test files.
//
// THIS FILE CARRIES NO BUILD CONSTRAINT, AND THAT IS THE POINT (go-qrdb#178).
// A helper defined in a constrained file and used from an unconstrained one
// breaks the package under every tag set that drops the definition -- and
// because `go vet ./...` and `go list ./...` are scoped to the CURRENT tag
// set, a suite that is green under one combination says nothing about the
// others. That is how two undefined symbols survived here across four tag
// combinations: the package was clean under exactly the one combination CI
// runs.
//
// A helper's constraint must be a SUPERSET of every caller's. The only
// constraint that satisfies that for a caller with no constraint at all is
// no constraint -- so shared helpers live here, and a helper only belongs in
// a constrained file when every one of its callers carries at least that
// same constraint.

// gapErrGetStorage wraps memStorage and fails Get while getErr is set.
//
// It DELEGATES once getErr is cleared rather than continuing to answer nil.
// That is what lets a test heal the storage and then read the state machine
// back: after a guard aborts on a read fault, "the key still holds what it
// held" and "the key resolves to nothing because this double answers nothing"
// are different facts, and a double that always answered nil could not tell
// them apart (go-qrdb#175).
type gapErrGetStorage struct {
	*memStorage
	getErr error
}

func (s *gapErrGetStorage) Get(key []byte) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.memStorage.Get(key)
}

// mustEncode unwraps an encoder's (command, error) pair for tests whose
// inputs are small and fixed. An error here is a TEST bug -- the encoders
// refuse only values their length fields cannot describe -- so it fails
// loudly rather than being threaded through every call site.
func mustEncode(cmd []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return cmd
}
