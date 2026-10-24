// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// maxTenantUpdateAttempts bounds how many times a tenant record write re-reads
// the record after losing its compare-and-swap to another writer. Each lost
// attempt means some other write of the same tenant committed, so the bound
// is reached only under sustained contention on one record; the request
// context bounds the retry as well.
const maxTenantUpdateAttempts = 8

// tenantRecordSwapper is the replicated compare-and-swap every tenant record
// write commits through: kvstore.AtomicKVStore's, or a seal backend's guarded
// swap.
type tenantRecordSwapper interface {
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
}

// tenantRecordReader is what each tenant service contributes to
// swapTenantRecord.
type tenantRecordReader interface {
	// readTenantRecord returns the stored bytes of tenantID's record and
	// their decoding.
	readTenantRecord(ctx context.Context, tenantID string) ([]byte, *TenantInfo, error)
}

// tenantRecordUpdater is what each tenant service contributes to
// updateTenantRecord.
type tenantRecordUpdater interface {
	tenantRecordReader

	// checkTenantUpdate runs the service's own checks of patch against info,
	// the record the update commits over.
	checkTenantUpdate(ctx context.Context, info *TenantInfo, patch TenantPatch) error

	beginStatusChange(ctx context.Context, tenantID string, from, to Status) (func(error), error)
}

// tenantRecordMutation applies one write to info, the tenant record as
// committed, and returns the settle function of what it began alongside the
// write (a status audit), which swapTenantRecord calls with the attempt's
// outcome. A nil settle means it began nothing. A mutation runs once per
// attempt, each time on a freshly read record.
type tenantRecordMutation func(info *TenantInfo) (settle func(error), err error)

// swapTenantRecord applies mutate to tenantID's record and commits it against
// the record it read. Every check a mutation makes runs against that same
// record, so a write built on a stale read can never commit: its swap loses,
// and it re-reads and re-applies the mutation to what was committed in
// between. A write that does not set the status therefore keeps a concurrent
// suspension.
//
// A lost attempt settles what its mutation began with *ConcurrentUpdateError,
// since that write was not committed. A store without compare-and-swap
// (swapper nil, or a swap answering seal.ErrGuardedSwapUnsupported) is
// refused with ErrTenantUpdateNotAtomic: a blind write in its place is the
// defect this closes.
func swapTenantRecord(ctx context.Context, r tenantRecordReader, swapper tenantRecordSwapper, tenantID string, mutate tenantRecordMutation) (*TenantInfo, error) {
	if swapper == nil {
		return nil, ErrTenantUpdateNotAtomic
	}
	key := metadataPrefix + tenantID
	for attempt := 1; attempt <= maxTenantUpdateAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, info, err := r.readTenantRecord(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		settle, err := mutate(info)
		if err != nil {
			return nil, err
		}
		if settle == nil {
			settle = func(error) {}
		}
		info.UpdatedAt = time.Now().UTC()

		next, err := json.Marshal(info)
		if err != nil {
			serr := &StoreError{Op: "marshal", Err: err}
			settle(serr)
			return nil, serr
		}
		swapped, err := swapper.CompareAndSwap(ctx, key, read, next)
		if errors.Is(err, seal.ErrGuardedSwapUnsupported) {
			settle(ErrTenantUpdateNotAtomic)
			return nil, ErrTenantUpdateNotAtomic
		}
		if err != nil {
			settle(err)
			return nil, &StoreError{Op: "put", Err: err}
		}
		if swapped {
			settle(nil)
			return info, nil
		}
		settle(&ConcurrentUpdateError{TenantID: tenantID, Attempts: attempt})
	}
	return nil, &ConcurrentUpdateError{TenantID: tenantID, Attempts: maxTenantUpdateAttempts}
}

// updateTenantRecord applies patch to tenantID's record through
// swapTenantRecord. The service's checks and the status transition run
// against the record the update commits over, so a status change is refused
// when the committed status no longer permits it, and only a patch that sets
// the status begins a status audit.
func updateTenantRecord(ctx context.Context, u tenantRecordUpdater, swapper tenantRecordSwapper, tenantID string, patch TenantPatch) (*TenantInfo, error) {
	return swapTenantRecord(ctx, u, swapper, tenantID, func(info *TenantInfo) (func(error), error) {
		if err := u.checkTenantUpdate(ctx, info, patch); err != nil {
			return nil, err
		}
		fromStatus := info.Status
		if err := applyStatus(info, patch); err != nil {
			return nil, err
		}
		if patch.IsolationType != "" {
			info.IsolationType = patch.IsolationType
		}
		if patch.IsolationPolicy != "" {
			info.IsolationPolicy = patch.IsolationPolicy
		}
		if patch.Name != "" {
			info.Name = patch.Name
		}
		applyDescriptive(info, patch)
		return u.beginStatusChange(ctx, tenantID, fromStatus, info.Status)
	})
}

// replaceQuotas and replaceEntitlements are UpdateQuotas' and
// UpdateEntitlements' writes.
func replaceQuotas(quotas *quota.Quotas) tenantRecordMutation {
	return func(info *TenantInfo) (func(error), error) {
		info.Quotas = quotas
		return nil, nil
	}
}

func replaceEntitlements(entitlements *quota.Entitlements) tenantRecordMutation {
	return func(info *TenantInfo) (func(error), error) {
		info.Entitlements = entitlements
		return nil, nil
	}
}

// incrementUsage is IncrementQuotaUsage's write: the limit is checked
// against the committed usage, so two concurrent increments can never both
// pass a limit only one of them fits under.
func incrementUsage(tenantID string, resource quota.Resource, delta int64) tenantRecordMutation {
	return func(info *TenantInfo) (func(error), error) {
		if info.Quotas == nil {
			info.Quotas = &quota.Quotas{}
		}
		current, err := info.Quotas.CurrentUsage(resource)
		if err != nil {
			return nil, &StoreError{Op: "increment-quota", Err: err}
		}
		limit, err := info.Quotas.Limit(resource)
		if err != nil {
			return nil, &StoreError{Op: "increment-quota", Err: err}
		}
		if limit > 0 && current+delta > limit {
			return nil, &QuotaExceededError{
				TenantID: tenantID,
				Resource: resource.String(),
				Current:  current,
				Delta:    delta,
				Limit:    limit,
			}
		}
		if err := info.Quotas.SetCurrentUsage(resource, current+delta); err != nil {
			return nil, &StoreError{Op: "increment-quota", Err: err}
		}
		return nil, nil
	}
}

// decrementUsage is DecrementQuotaUsage's write; usage floors at zero.
func decrementUsage(resource quota.Resource, delta int64) tenantRecordMutation {
	return func(info *TenantInfo) (func(error), error) {
		if info.Quotas == nil {
			info.Quotas = &quota.Quotas{}
		}
		current, err := info.Quotas.CurrentUsage(resource)
		if err != nil {
			return nil, &StoreError{Op: "decrement-quota", Err: err}
		}
		if err := info.Quotas.SetCurrentUsage(resource, max(current-delta, 0)); err != nil {
			return nil, &StoreError{Op: "decrement-quota", Err: err}
		}
		return nil, nil
	}
}

// setUsage is SetQuotaUsage's write.
func setUsage(resource quota.Resource, value int64) tenantRecordMutation {
	return func(info *TenantInfo) (func(error), error) {
		if info.Quotas == nil {
			info.Quotas = &quota.Quotas{}
		}
		if err := info.Quotas.SetCurrentUsage(resource, value); err != nil {
			return nil, &StoreError{Op: "set-quota-usage", Err: err}
		}
		return nil, nil
	}
}
