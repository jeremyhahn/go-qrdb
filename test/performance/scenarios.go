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
// performance benchmarks.
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

	// etcd API benchmark scenarios (1N).
	ScenarioEtcdPutSEQ       = "etcd_put_seq"
	ScenarioEtcdPutConc8     = "etcd_put_conc8"
	ScenarioEtcdGetSEQ       = "etcd_get_seq"
	ScenarioEtcdGetConc8     = "etcd_get_conc8"
	ScenarioEtcdDeleteSEQ    = "etcd_delete_seq"
	ScenarioEtcdDeleteConc8  = "etcd_delete_conc8"
	ScenarioEtcdTxnSEQ       = "etcd_txn_seq"
	ScenarioEtcdWatchLatency = "etcd_watch_latency"

	// gRPC benchmark scenarios (1N).
	ScenarioKVGRPCPutSEQ      = "kv_grpc_put_seq"
	ScenarioKVGRPCPutConc8    = "kv_grpc_put_conc8"
	ScenarioKVGRPCGetSEQ      = "kv_grpc_get_seq"
	ScenarioKVGRPCGetConc8    = "kv_grpc_get_conc8"
	ScenarioKVGRPCDeleteSEQ   = "kv_grpc_delete_seq"
	ScenarioKVGRPCDeleteConc8 = "kv_grpc_delete_conc8"
	ScenarioKVGRPCBatchPut10K = "kv_grpc_batch_put_10k"
	ScenarioKVGRPCScan10K     = "kv_grpc_scan_10k"

	// Coalesced gRPC benchmark scenarios (1N) — client-side batching.
	// Note: Coalesced GET is disabled because the server's Batch RPC routes
	// GET through MVCCGetQuery, incompatible with the raw PebbleDB SM.
	ScenarioKVGRPCPutCoalesced    = "kv_grpc_put_coalesced"
	ScenarioKVGRPCDeleteCoalesced = "kv_grpc_delete_coalesced"

	// Native QUIC benchmark scenarios (1N).
	ScenarioKVQUICPutSEQ      = "kv_quic_put_seq"
	ScenarioKVQUICPutConc8    = "kv_quic_put_conc8"
	ScenarioKVQUICGetSEQ      = "kv_quic_get_seq"
	ScenarioKVQUICGetConc8    = "kv_quic_get_conc8"
	ScenarioKVQUICDeleteSEQ   = "kv_quic_delete_seq"
	ScenarioKVQUICDeleteConc8 = "kv_quic_delete_conc8"
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
	ScenarioEtcdPutSEQ,
	ScenarioEtcdPutConc8,
	ScenarioEtcdGetSEQ,
	ScenarioEtcdGetConc8,
	ScenarioEtcdDeleteSEQ,
	ScenarioEtcdDeleteConc8,
	ScenarioEtcdTxnSEQ,
	ScenarioEtcdWatchLatency,
	ScenarioKVGRPCPutSEQ,
	ScenarioKVGRPCPutConc8,
	ScenarioKVGRPCGetSEQ,
	ScenarioKVGRPCGetConc8,
	ScenarioKVGRPCDeleteSEQ,
	ScenarioKVGRPCDeleteConc8,
	ScenarioKVGRPCBatchPut10K,
	ScenarioKVGRPCScan10K,
	ScenarioKVGRPCPutCoalesced,
	ScenarioKVGRPCDeleteCoalesced,
	ScenarioKVQUICPutSEQ,
	ScenarioKVQUICPutConc8,
	ScenarioKVQUICGetSEQ,
	ScenarioKVQUICGetConc8,
	ScenarioKVQUICDeleteSEQ,
	ScenarioKVQUICDeleteConc8,
}
