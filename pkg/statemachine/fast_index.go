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

import (
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// handleIndexCreate decodes an IndexCreate command and delegates to the
// configured IndexManager. Returns the Raft revision on success.
func (f *FastStateMachine) handleIndexCreate(payload []byte, revision uint64) (sm.Result, error) {
	if f.indexManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "index"}
	}

	name, fieldName, opts, err := protocol.DecodeIndexCreateOpts(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "index create: " + err.Error()}
	}

	if err := f.indexManager.CreateIndex(name, fieldName, opts); err != nil {
		return indexManagerFailure("index-create", err)
	}

	return sm.Result{Value: revision}, nil
}

// handleIndexDrop decodes an IndexDrop command and delegates to the
// configured IndexManager. Returns the Raft revision on success.
func (f *FastStateMachine) handleIndexDrop(payload []byte, revision uint64) (sm.Result, error) {
	if f.indexManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "index"}
	}

	name, dropOpts, err := protocol.DecodeIndexDropOpts(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "index drop: " + err.Error()}
	}

	if err := f.indexManager.DropIndex(name, dropOpts); err != nil {
		return indexManagerFailure("index-drop", err)
	}

	return sm.Result{Value: revision}, nil
}

// lookupIndex handles read-only index queries during Lookup. It returns
// the query result and true if the query type was recognized, or
// (nil, false) if the query is not an index query.
func (f *FastStateMachine) lookupIndex(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case index.IndexQueryRequest:
		if f.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := f.indexManager.Query(q.IndexName, q.Value)
		return result, true, err

	case index.IndexScanRequest:
		if f.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := f.indexManager.Scan(q.IndexName, q.Start, q.End)
		return result, true, err

	case index.IndexListRequest:
		if f.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := f.indexManager.List()
		return result, true, err

	default:
		return nil, false, nil
	}
}
