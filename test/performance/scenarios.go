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

// Package kvperf defines scenario key constants for KV database
// performance benchmarks comparing QRDB against TiKV.
package kvperf

// Scenario key constants for KV-database-level benchmarks.
const (
	// Single-node (1N) scenarios.
	ScenarioKVPutSEQ      = "kv_put_seq"
	ScenarioKVPutConc8    = "kv_put_conc8"
	ScenarioKVGetSEQ      = "kv_get_seq"
	ScenarioKVGetConc8    = "kv_get_conc8"
	ScenarioKVDeleteSEQ   = "kv_delete_seq"
	ScenarioKVDeleteConc8 = "kv_delete_conc8"
	ScenarioKVBatchPut10K = "kv_batch_put_10k"
	ScenarioKVScan10K     = "kv_scan_10k"

	// Multi-node 3N scenarios.
	ScenarioKVPutSEQ3N   = "kv_put_seq_3n"
	ScenarioKVPutConc83N = "kv_put_conc8_3n"
	ScenarioKVGetSEQ3N   = "kv_get_seq_3n"
	ScenarioKVGetConc83N = "kv_get_conc8_3n"
)

// AllKVScenarios lists all KV benchmark scenario keys in display order.
var AllKVScenarios = []string{
	ScenarioKVPutSEQ,
	ScenarioKVPutConc8,
	ScenarioKVGetSEQ,
	ScenarioKVGetConc8,
	ScenarioKVDeleteSEQ,
	ScenarioKVDeleteConc8,
	ScenarioKVBatchPut10K,
	ScenarioKVScan10K,
	ScenarioKVPutSEQ3N,
	ScenarioKVPutConc83N,
	ScenarioKVGetSEQ3N,
	ScenarioKVGetConc83N,
}
