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

// This file carries NO build constraint, unlike storage.go and service.go,
// which are behind "qrdb_ts". The key layout is a contract two other packages
// name unconditionally -- pkg/service's internal-key guard and the database
// teardown that reclaims a dropped database's series -- and both compile in
// every tag combination. Declaring these beside the engine would delete them
// from the default build and take those callers with them, so the constants
// live here and storage.go references them (the build-constraint superset
// rule: move the shared symbol up, never push the constraint down).

// The time-series key prefixes are EXPORTED because two other packages must
// name the same byte ranges and neither can be allowed to drift from them:
// pkg/service's internal-key guard, which keeps them out of user
// enumerations, and the database teardown that reclaims a dropped database's
// series. Both reference these constants rather than repeating the literals,
// so the layout has exactly one declaration.
//
// Note which of the four carry the metric name, because that is what makes a
// key attributable to a database: points, the metric index and the retention
// record all embed the metric, while KeyPrefixTagIndex is keyed by tag
// key:value alone and is therefore shared by every metric in every database.
const (
	// KeyPrefixPoints is the namespace prefix for all time-series data point
	// keys: KeyPrefixPoints + metric + ":" + timestamp + ":" + tag hash.
	KeyPrefixPoints = "ts:"

	// KeyPrefixMetricIndex is the namespace prefix for the metric name index:
	// KeyPrefixMetricIndex + metric.
	KeyPrefixMetricIndex = "idx:metric:"

	// KeyPrefixTagIndex is the namespace prefix for tag key:value indexes:
	// KeyPrefixTagIndex + tagKey + ":" + tagValue. It carries no metric, so
	// no database owns it.
	KeyPrefixTagIndex = "idx:tag:"

	// KeyPrefixRetention is the namespace prefix for retention policy
	// storage: KeyPrefixRetention + metric.
	KeyPrefixRetention = "retention:"
)
