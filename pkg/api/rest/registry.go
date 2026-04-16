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

package rest

import "github.com/jeremyhahn/go-qrdb/pkg/service"

// ServiceRegistry resolves per-(tenant, database) service instances for
// data-plane request routing. Each method returns the service scoped to
// the given (tenant, db) pair, or a typed error when the tenant or
// database does not exist.
//
// Implementations must be safe for concurrent use.
type ServiceRegistry interface {
	// KVService returns the KVServicer for the given (tenant, db) scope.
	KVService(tenant, db string) (service.KVServicer, error)

	// MVCCService returns the MVCCKVServicer for the given scope.
	MVCCService(tenant, db string) (service.MVCCKVServicer, error)

	// WatchService returns the WatchServicer for the given scope.
	WatchService(tenant, db string) (service.WatchServicer, error)

	// LeaseService returns the LeaseServicer for the given scope.
	LeaseService(tenant, db string) (service.LeaseServicer, error)

	// IndexService returns the IndexServicer for the given scope.
	IndexService(tenant, db string) (service.IndexServicer, error)

	// TimeSeriesService returns the TimeSeriesServicer for the given scope.
	TimeSeriesService(tenant, db string) (service.TimeSeriesServicer, error)

	// VectorService returns the VectorServicer for the given scope.
	VectorService(tenant, db string) (service.VectorServicer, error)

	// TransactionService returns the TransactionServicer for the given scope.
	TransactionService(tenant, db string) (service.TransactionServicer, error)

	// BatchService returns the BatchServicer for the given scope.
	BatchService(tenant, db string) (service.BatchServicer, error)
}
