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
	"sort"

	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// Lookup reads state without going through Raft. Supported query types:
//   - []byte: point lookup by key (returns []byte value or nil)
//   - *PrefixQuery: prefix scan returning map[string][]byte
//   - kv.ListQuery: returns sorted []string of keys matching the prefix
//   - index.IndexQueryRequest: exact-match index query
//   - index.IndexScanRequest: range index scan
//   - index.IndexListRequest: list all indexes
//   - lease.LeaseTimeToLiveQuery: lease TTL lookup
//   - lease.LeasesQuery: list all leases
//   - MVCCGetQuery: MVCC point lookup
//   - MVCCGetAtRevisionQuery: MVCC point lookup at revision
//   - MVCCRangeQuery: MVCC range query
//   - MVCCRevisionQuery: current MVCC revision
//   - MVCCScanChangesQuery: keys changed in a revision window
//   - timeseries.TSQueryRequest: time-series range query
//   - timeseries.TSAggregateRequest: time-series aggregation
//   - timeseries.TSRichQueryRequest: rich time-series query
//   - timeseries.TSGetRetentionRequest: retention policy lookup
//   - timeseries.TSListMetricsRequest: list metrics by prefix
func (k *KVStateMachine) Lookup(query interface{}) (interface{}, error) {
	// Try index lookups.
	if result, handled, err := k.lookupIndex(query); handled {
		return result, err
	}

	// Try lease lookups.
	if result, handled, err := k.lookupLease(query); handled {
		return result, err
	}

	// Try import-staging lookups.
	if result, handled, err := k.lookupImportStage(query); handled {
		return result, err
	}

	// Try MVCC lookups.
	if result, handled, err := k.lookupMVCC(query); handled {
		return result, err
	}

	// Try time-series lookups.
	if result, handled, err := k.lookupTimeSeries(query); handled {
		return result, err
	}

	switch q := query.(type) {
	case []byte:
		return k.lookupKey(q)
	case *PrefixQuery:
		return k.lookupPrefix(q)
	case kv.ListQuery:
		return k.listKeys(q.Prefix)
	default:
		return nil, &IncompleteDataError{Reason: "unsupported query type"}
	}
}

// lookupKey performs a point lookup via the backend. A state machine key is
// not client data and reads as absent.
func (k *KVStateMachine) lookupKey(key []byte) (interface{}, error) {
	if isStateMachineKey(key) {
		return nil, nil
	}
	val, err := k.backend.Get(key)
	if err != nil {
		return nil, err
	}
	if val == nil {
		return nil, nil
	}
	return val, nil
}

// lookupPrefix performs a prefix scan via the backend. The state machine's
// own records (isStateMachineKey) are not returned: they are not client data.
// A limit counts only what is returned, so the backend is asked for every
// match when the prefix can reach those records.
func (k *KVStateMachine) lookupPrefix(pq *PrefixQuery) (interface{}, error) {
	limit := pq.Limit
	if reachesStateMachineKeys(pq.Prefix) {
		limit = 0
	}
	data, err := k.backend.Scan(pq.Prefix, limit)
	if err != nil {
		return nil, err
	}
	for key := range data {
		if isStateMachineKey([]byte(key)) {
			delete(data, key)
		}
	}
	if pq.Limit > 0 && len(data) > pq.Limit {
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys[pq.Limit:] {
			delete(data, key)
		}
	}
	return data, nil
}

// listKeys returns a sorted slice of keys matching the given prefix.
func (k *KVStateMachine) listKeys(prefix string) ([]string, error) {
	keys, err := k.backend.List([]byte(prefix))
	if err != nil {
		return nil, err
	}

	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if isStateMachineKey(key) {
			continue
		}
		result = append(result, string(key))
	}
	sort.Strings(result)
	return result, nil
}

// ---------------------------------------------------------------------------
// Index lookups
// ---------------------------------------------------------------------------

func (k *KVStateMachine) lookupIndex(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case index.IndexQueryRequest:
		if k.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := k.indexManager.Query(q.IndexName, q.Value)
		return result, true, err

	case index.IndexScanRequest:
		if k.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := k.indexManager.Scan(q.IndexName, q.Start, q.End)
		return result, true, err

	case index.IndexListRequest:
		if k.indexManager == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "index"}
		}
		result, err := k.indexManager.List()
		return result, true, err

	default:
		return nil, false, nil
	}
}

// ---------------------------------------------------------------------------
// MVCC lookups
// ---------------------------------------------------------------------------

func (k *KVStateMachine) lookupMVCC(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case MVCCGetQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := k.mvccStore.Range(q.Key, nil, 0, 1, false, false)
		return result, true, err

	case *MVCCGetQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := k.mvccStore.Range(q.Key, nil, 0, 1, false, false)
		return result, true, err

	case MVCCGetAtRevisionQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := k.mvccStore.Range(q.Key, nil, q.Revision, 1, false, false)
		return result, true, err

	case *MVCCGetAtRevisionQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := k.mvccStore.Range(q.Key, nil, q.Revision, 1, false, false)
		return result, true, err

	case MVCCRangeQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := k.mvccStore.Range(q.Start, q.End, q.Revision, q.Limit, q.CountOnly, q.KeysOnly)
		return result, true, err

	case *MVCCRangeQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := k.mvccStore.Range(q.Start, q.End, q.Revision, q.Limit, q.CountOnly, q.KeysOnly)
		return result, true, err

	case MVCCRevisionQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		_, rev, err := k.mvccStore.Range(nil, nil, 0, 0, true, false)
		return rev, true, err

	case *MVCCRevisionQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		_, rev, err := k.mvccStore.Range(nil, nil, 0, 0, true, false)
		return rev, true, err

	case MVCCScanChangesQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, err := k.mvccStore.ScanChanges(q.StartRevision, q.EndRevision, q.Prefix, q.Limit)
		return result, true, err

	case *MVCCScanChangesQuery:
		if k.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, err := k.mvccStore.ScanChanges(q.StartRevision, q.EndRevision, q.Prefix, q.Limit)
		return result, true, err

	default:
		return nil, false, nil
	}
}

// ---------------------------------------------------------------------------
// Time-series lookups
// ---------------------------------------------------------------------------

func (k *KVStateMachine) lookupTimeSeries(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case timeseries.TSQueryRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.QueryRange(q.Metric, q.Start.UnixNano(), q.End.UnixNano())
		return result, true, err

	case *timeseries.TSQueryRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.QueryRange(q.Metric, q.Start.UnixNano(), q.End.UnixNano())
		return result, true, err

	case timeseries.TSAggregateRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.QueryAggregate(q.Metric, q.Start.UnixNano(), q.End.UnixNano(), byte(q.Func), q.Interval.Nanoseconds())
		return result, true, err

	case *timeseries.TSAggregateRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.QueryAggregate(q.Metric, q.Start.UnixNano(), q.End.UnixNano(), byte(q.Func), q.Interval.Nanoseconds())
		return result, true, err

	case timeseries.TSRichQueryRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.QueryRich(q.Request)
		return result, true, err

	case *timeseries.TSRichQueryRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.QueryRich(q.Request)
		return result, true, err

	case timeseries.TSGetRetentionRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.GetRetentionPolicy(q.Metric)
		return result, true, err

	case *timeseries.TSGetRetentionRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.GetRetentionPolicy(q.Metric)
		return result, true, err

	case timeseries.TSListMetricsRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.ListMetrics(q.Prefix, q.Limit)
		return result, true, err

	case *timeseries.TSListMetricsRequest:
		if k.tsStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "timeseries"}
		}
		result, err := k.tsStore.ListMetrics(q.Prefix, q.Limit)
		return result, true, err

	default:
		return nil, false, nil
	}
}
