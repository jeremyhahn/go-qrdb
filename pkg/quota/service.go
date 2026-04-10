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

package quota

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// Compile-time interface check.
var _ Checker = (*Service)(nil)

// ErrNilProvider is returned when a nil QuotaProvider is passed to
// NewService.
var ErrNilProvider = errors.New("quota: nil provider")

// Service enforces per-tenant quota limits by delegating quota lookups
// to a QuotaProvider and current-usage queries to registered CountFuncs.
type Service struct {
	provider QuotaProvider
	counters sync.Map // Resource → CountFunc
	logger   *slog.Logger
}

// NewService creates a new Service backed by the given QuotaProvider.
// Returns ErrNilProvider if provider is nil. A nil logger falls back to
// slog.Default().
func NewService(provider QuotaProvider, logger *slog.Logger) (*Service, error) {
	if provider == nil {
		return nil, ErrNilProvider
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		provider: provider,
		logger:   logger,
	}, nil
}

// RegisterCounter registers a CountFunc for the given resource type.
// Subsequent Check, CheckN, and Usage calls for that resource will
// invoke fn to obtain the current usage. Registering a second function
// for the same resource replaces the previous one.
func (s *Service) RegisterCounter(resource Resource, fn CountFunc) {
	s.counters.Store(resource, fn)
}

// Check returns nil if the tenant has capacity for one more of the given
// resource. Returns *ExceededError if the limit is reached, or another
// error if the quota or counter cannot be resolved.
func (s *Service) Check(ctx context.Context, tenantID string, resource Resource) error {
	return s.CheckN(ctx, tenantID, resource, 1)
}

// CheckN returns nil if the tenant has capacity for n additional units
// of the given resource. Returns *ExceededError if adding n would exceed
// the configured limit.
func (s *Service) CheckN(ctx context.Context, tenantID string, resource Resource, n int64) error {
	quotas, err := s.provider.Quotas(ctx, tenantID)
	if err != nil {
		return err
	}

	limit, err := quotas.Limit(resource)
	if err != nil {
		return err
	}

	// -1 means unlimited; skip counter lookup entirely.
	if limit == -1 {
		return nil
	}

	// 0 means use system default; treat as unlimited for now.
	if limit == 0 {
		return nil
	}

	current, err := s.usageFor(ctx, tenantID, resource)
	if err != nil {
		return err
	}

	if current+n > limit {
		s.logger.Warn("quota exceeded",
			slog.String("tenant_id", tenantID),
			slog.String("resource", resource.String()),
			slog.Int64("current", current),
			slog.Int64("requested", n),
			slog.Int64("limit", limit))
		return &ExceededError{
			TenantID: tenantID,
			Resource: resource,
			Current:  current,
			Limit:    limit,
		}
	}

	return nil
}

// Usage returns the current count for the given resource and tenant by
// invoking the registered CountFunc. Returns UnregisteredCounterError
// if no counter has been registered for the resource.
func (s *Service) Usage(ctx context.Context, tenantID string, resource Resource) (int64, error) {
	return s.usageFor(ctx, tenantID, resource)
}

// usageFor invokes the registered CountFunc for the resource.
func (s *Service) usageFor(ctx context.Context, tenantID string, resource Resource) (int64, error) {
	val, ok := s.counters.Load(resource)
	if !ok {
		return 0, &UnregisteredCounterError{Resource: resource}
	}
	fn := val.(CountFunc)
	return fn(ctx, tenantID)
}
