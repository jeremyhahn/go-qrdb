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

// Package tenantconv converts protobuf tenant messages into the
// transport-neutral tenant types. It is shared by the gRPC and Unix
// (gRPC-over-UDS) transports, which exchange the SAME protobuf types, so the
// conversion lives in exactly one place instead of being duplicated per
// transport. It is internal: only the transport implementations may import it.
package tenantconv

import (
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ToTenantInfo converts a protobuf TenantResponse into a transport.TenantInfo.
// A nil response yields the zero value. Quotas and Entitlements are populated
// only when present on the wire; timestamps are parsed best-effort (a malformed
// timestamp leaves the corresponding field at its zero value).
func ToTenantInfo(resp *pb.TenantResponse) transport.TenantInfo {
	if resp == nil {
		return transport.TenantInfo{}
	}

	info := transport.TenantInfo{
		ID:            resp.GetId(),
		Name:          resp.GetName(),
		IsolationType: transport.TenantIsolationType(resp.GetIsolationType()),
	}

	if pbQ := resp.GetQuotas(); pbQ != nil {
		info.Quotas = &transport.TenantQuotas{
			MaxDatabases:          pbQ.GetMaxDatabases(),
			MaxRaftGroups:         pbQ.GetMaxRaftGroups(),
			MaxReplicasPerGroup:   pbQ.GetMaxReplicasPerGroup(),
			MaxTotalReplicas:      pbQ.GetMaxTotalReplicas(),
			MaxCollections:        pbQ.GetMaxCollections(),
			MaxKeys:               pbQ.GetMaxKeys(),
			MaxStorageBytes:       pbQ.GetMaxStorageBytes(),
			MaxDatabaseSizeBytes:  pbQ.GetMaxDatabaseSizeBytes(),
			MaxUsers:              pbQ.GetMaxUsers(),
			MaxRoles:              pbQ.GetMaxRoles(),
			MaxCertificates:       pbQ.GetMaxCertificates(),
			MaxBackups:            pbQ.GetMaxBackups(),
			MaxVectorDims:         pbQ.GetMaxVectorDims(),
			MaxTSMetrics:          pbQ.GetMaxTsMetrics(),
			SystemBarrierQuota:    pbQ.GetSystemBarrierQuota(),
			MaxDedicatedWALGroups: pbQ.GetMaxDedicatedWalGroups(),
		}
	}

	if pbE := resp.GetEntitlements(); pbE != nil {
		info.Entitlements = &transport.TenantEntitlements{
			AllowedIsolationModes:     pbE.GetAllowedIsolationModes(),
			AllowDedicatedRaft:        pbE.GetAllowDedicatedRaft(),
			AllowVolumeStorage:        pbE.GetAllowVolumeStorage(),
			AllowSystemManagedBarrier: pbE.GetAllowSystemManagedBarrier(),
			AllowedBarrierModes:       pbE.GetAllowedBarrierModes(),
			AllowDedicatedWAL:         pbE.GetAllowDedicatedWal(),
		}
	}

	if ts := resp.GetCreatedAt(); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			info.CreatedAt = parsed
		}
	}
	if ts := resp.GetUpdatedAt(); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			info.UpdatedAt = parsed
		}
	}

	return info
}
