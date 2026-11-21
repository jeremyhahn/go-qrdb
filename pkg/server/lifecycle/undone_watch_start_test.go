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

//go:build !integration

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// reconcilingTenantServicer is a tenant service that also reconciles undone
// and deleted tenants.
type reconcilingTenantServicer struct {
	*fakeTenantServicer
	*recordingReconciler
}

// nodeWatchService gives a node the prefix watches of a queueWatchService;
// every other watch method is unused by the undone-tenant watch.
type nodeWatchService struct {
	service.WatchServicer
	q *queueWatchService
}

func (w nodeWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return w.q.WatchPrefix(ctx, prefix, opts...)
}

// TestStartUndoneWatch_TenantWithoutReconcilerIsSkipped proves a node whose
// tenant service cannot reconcile undone tenants opens no watch.
func TestStartUndoneWatch_TenantWithoutReconcilerIsSkipped(t *testing.T) {
	ws := &queueWatchService{errs: []error{errors.New("opened a watch")}}
	n := &Node{Watch: nodeWatchService{q: ws}, Tenant: &fakeTenantServicer{}}
	if err := startUndoneWatch(context.Background(), n); err != nil {
		t.Fatalf("startUndoneWatch = %v; want nil, no watch opened", err)
	}
}

// TestStartUndoneWatch_OpensTheWatch proves a node whose tenant service
// reconciles undone tenants opens the watch, and a refused open is reported.
func TestStartUndoneWatch_OpensTheWatch(t *testing.T) {
	boom := errors.New("no hub")
	ws := &queueWatchService{errs: []error{boom}}
	n := &Node{Watch: nodeWatchService{q: ws}, Tenant: reconcilingTenantServicer{&fakeTenantServicer{}, newRecordingReconciler()}}
	err := startUndoneWatch(context.Background(), n)
	var typed *UndoneTenantWatchError
	if !errors.As(err, &typed) || !errors.Is(err, boom) {
		t.Fatalf("startUndoneWatch = %v; want *UndoneTenantWatchError wrapping %v", err, boom)
	}
}
