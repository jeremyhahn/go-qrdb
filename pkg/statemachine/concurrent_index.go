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
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// Index write handlers (called after Raft consensus via Update)
// ---------------------------------------------------------------------------

func (c *ConcurrentStateMachine) handleIndexCreate(payload []byte, revision uint64) (sm.Result, error) {
	if c.indexManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "index"}
	}

	name, fieldName, unique, sparse, numeric, err := protocol.DecodeIndexCreate(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "index create: " + err.Error()}
	}

	if createErr := c.indexManager.CreateIndex(name, fieldName, unique, sparse, numeric); createErr != nil {
		return sm.Result{}, &StorageError{Op: "index-create", Inner: createErr}
	}

	return sm.Result{Value: revision}, nil
}

func (c *ConcurrentStateMachine) handleIndexDrop(payload []byte, revision uint64) (sm.Result, error) {
	if c.indexManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "index"}
	}

	name, err := protocol.DecodeIndexDrop(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "index drop: " + err.Error()}
	}

	if dropErr := c.indexManager.DropIndex(name); dropErr != nil {
		return sm.Result{}, &StorageError{Op: "index-drop", Inner: dropErr}
	}

	return sm.Result{Value: revision}, nil
}

// ---------------------------------------------------------------------------
// Index read handlers (called from Lookup without Raft consensus)
// ---------------------------------------------------------------------------

func (c *ConcurrentStateMachine) concurrentLookupIndex(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case index.IndexQueryRequest:
		if c.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := c.indexManager.Query(q.IndexName, q.Value)
		return result, true, err

	case index.IndexScanRequest:
		if c.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := c.indexManager.Scan(q.IndexName, q.Start, q.End)
		return result, true, err

	case index.IndexListRequest:
		if c.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := c.indexManager.List()
		return result, true, err

	default:
		return nil, false, nil
	}
}
