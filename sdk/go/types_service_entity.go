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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/service"

// ---------------------------------------------------------------------------
// Entity request/response types (service layer)
// ---------------------------------------------------------------------------

// PutEntityRequest is the request for a KV put operation.
type PutEntityRequest = service.PutEntityRequest

// PutEntityResponse is the response for a KV put operation.
type PutEntityResponse = service.PutEntityResponse

// GetEntityRequest is the request for a KV get operation.
type GetEntityRequest = service.GetEntityRequest

// GetEntityResponse is the response for a KV get operation.
type GetEntityResponse = service.GetEntityResponse

// DeleteEntityRequest is the request for a KV delete operation.
type DeleteEntityRequest = service.DeleteEntityRequest

// DeleteEntityResponse is the response for a KV delete operation.
type DeleteEntityResponse = service.DeleteEntityResponse

// ListEntitiesRequest is the request for a KV list operation.
type ListEntitiesRequest = service.ListEntitiesRequest

// ListEntitiesResponse is the response for a KV list operation.
type ListEntitiesResponse = service.ListEntitiesResponse

// ---------------------------------------------------------------------------
// Admin shard/topology request/response types (service layer)
// ---------------------------------------------------------------------------

// StartShardRequest is the request to start a shard.
type StartShardRequest = service.StartShardRequest

// ShardStateResponse reports the state of a shard.
type ShardStateResponse = service.ShardStateResponse

// TopologyResponse reports the cluster topology.
type TopologyResponse = service.TopologyResponse

// ---------------------------------------------------------------------------
// Service errors
// ---------------------------------------------------------------------------

// KeyNotFoundError is returned when a requested key does not exist.
type KeyNotFoundError = service.KeyNotFoundError
