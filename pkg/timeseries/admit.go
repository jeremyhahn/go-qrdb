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

package timeseries

import "github.com/jeremyhahn/go-qrdb/pkg/storage"

// AdmitMetrics reports whether a write naming metrics may apply over r
// without the shard holding more than maxMetrics distinct metric names. A
// maxMetrics of zero is no cap.
//
// It runs in the apply path, over the reader the write itself goes through
// (a batch reads its own writes), so every replica decides it from the same
// replicated state in log order: two writes proposed through different nodes
// cannot both pass a cap that only one of them fits under. A write naming
// only metrics the shard already holds is always admitted.
func AdmitMetrics(r storage.Reader, metrics []string, maxMetrics uint32) error {
	if maxMetrics == 0 || len(metrics) == 0 {
		return nil
	}
	fresh := make(map[string]struct{}, len(metrics))
	for _, metric := range metrics {
		if metric == "" {
			continue
		}
		if _, seen := fresh[metric]; seen {
			continue
		}
		held, err := r.Get([]byte(KeyPrefixMetricIndex + metric))
		if err != nil {
			return &ReadError{Err: err}
		}
		if held == nil {
			fresh[metric] = struct{}{}
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	if uint64(len(fresh)) > uint64(maxMetrics) {
		return &MetricLimitError{Max: maxMetrics}
	}
	room := int(maxMetrics) - len(fresh)
	held, err := countMetrics(r, room+1)
	if err != nil {
		return err
	}
	if held > room {
		return &MetricLimitError{Max: maxMetrics}
	}
	return nil
}

// countMetrics counts the metric names r holds, stopping at limit.
func countMetrics(r storage.Reader, limit int) (int, error) {
	iter := r.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte(KeyPrefixMetricIndex),
		UpperBound: []byte(KeyPrefixMetricIndex + "\xff"),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	held := 0
	for ok := iter.SeekToFirst(); ok && iter.Valid() && held < limit; ok = iter.Next() {
		if len(iter.Key()) > len(KeyPrefixMetricIndex) {
			held++
		}
	}
	if err := iter.Error(); err != nil {
		return 0, &ReadError{Err: err}
	}
	return held, nil
}
