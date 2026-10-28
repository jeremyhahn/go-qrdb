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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// vlogCalls drives every VlogClient method with a well-formed request, or
// with a nil one when nilReq is set, and returns each call's error by method.
func vlogCalls(ctx context.Context, c transport.VlogClient, nilReq bool) map[string]error {
	log := "audit"
	out := map[string]error{}
	var err error
	if !nilReq {
		_, err = c.VlogCreate(ctx, &vlogwire.CreateRequest{Log: log, Origin: "o"})
	} else {
		_, err = c.VlogCreate(ctx, nil)
	}
	out["VlogCreate"] = err
	if !nilReq {
		_, err = c.VlogAppend(ctx, &vlogwire.AppendRequest{Log: log, Entries: [][]byte{[]byte("x")}})
	} else {
		_, err = c.VlogAppend(ctx, nil)
	}
	out["VlogAppend"] = err
	if !nilReq {
		_, err = c.VlogGet(ctx, &vlogwire.GetRequest{Log: log})
	} else {
		_, err = c.VlogGet(ctx, nil)
	}
	out["VlogGet"] = err
	if !nilReq {
		_, err = c.VlogProveInclusion(ctx, &vlogwire.InclusionRequest{Log: log, Size: 1})
	} else {
		_, err = c.VlogProveInclusion(ctx, nil)
	}
	out["VlogProveInclusion"] = err
	if !nilReq {
		_, err = c.VlogProveConsistency(ctx, &vlogwire.ConsistencyRequest{Log: log, Size1: 1, Size2: 1})
	} else {
		_, err = c.VlogProveConsistency(ctx, nil)
	}
	out["VlogProveConsistency"] = err
	if !nilReq {
		_, err = c.VlogHead(ctx, &vlogwire.LogRequest{Log: log})
	} else {
		_, err = c.VlogHead(ctx, nil)
	}
	out["VlogHead"] = err
	if !nilReq {
		_, err = c.VlogSignHead(ctx, &vlogwire.LogRequest{Log: log})
	} else {
		_, err = c.VlogSignHead(ctx, nil)
	}
	out["VlogSignHead"] = err
	if !nilReq {
		_, err = c.VlogRotateKey(ctx, &vlogwire.RotateKeyRequest{Log: log, SigningKeyType: "ed25519"})
	} else {
		_, err = c.VlogRotateKey(ctx, nil)
	}
	out["VlogRotateKey"] = err
	if !nilReq {
		_, err = c.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{Log: log, Cutoff: 1})
	} else {
		_, err = c.VlogExpireBodies(ctx, nil)
	}
	out["VlogExpireBodies"] = err
	return out
}

func TestVlog_NoLogServiceIsANilServiceError(t *testing.T) {
	tr := newExtTransport(t)
	errs := vlogCalls(context.Background(), tr, false)
	_, err := tr.VlogList(context.Background(), &vlogwire.ListRequest{})
	errs["VlogList"] = err
	for method, err := range errs {
		var nse *NilServiceError
		if !errors.As(err, &nse) || nse.Service != "LogService" {
			t.Errorf("%s: err = %v, want NilServiceError{LogService}", method, err)
		}
	}
}

func TestVlog_NilRequestIsRefusedBeforeTheService(t *testing.T) {
	tr := newExtTransport(t)
	for method, err := range vlogCalls(context.Background(), tr, true) {
		var nre *NilRequestError
		if !errors.As(err, &nre) || nre.Method != method {
			t.Errorf("%s: err = %v, want NilRequestError{%s}", method, err, method)
		}
	}
}

func TestVlog_ClosedTransportRefusesEveryMethod(t *testing.T) {
	tr := closedExtTransport(t)
	errs := vlogCalls(context.Background(), tr, false)
	_, err := tr.VlogList(context.Background(), nil)
	errs["VlogList"] = err
	for method, err := range errs {
		var ce *transport.ClosedError
		if !errors.As(err, &ce) {
			t.Errorf("%s: err = %v, want ClosedError", method, err)
		}
	}
}

func TestVlog_DatabaseDefaultsToTheDefaultScope(t *testing.T) {
	tr := newExtTransport(t)
	if got := tr.vlogRef("l"); got.Database != transport.DefaultDatabaseScope || got.Log != "l" || got.TenantID != "" {
		t.Fatalf("vlogRef = %+v, want the default database, log l and no tenant", got)
	}
}

func TestVlog_WithDatabaseScopesTheLogDatabase(t *testing.T) {
	tr := newExtTransport(t, WithServiceRegistry(newScopeRegistry()))
	scoped, err := tr.WithDatabase("orders")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	st, ok := scoped.(*Transport)
	if !ok {
		t.Fatalf("WithDatabase returned %T, want *Transport", scoped)
	}
	if got := st.vlogRef("l").Database; got != "orders" {
		t.Fatalf("scoped database = %q, want orders", got)
	}
	if got := tr.vlogRef("l").Database; got != transport.DefaultDatabaseScope {
		t.Fatalf("WithDatabase changed the parent's database to %q", got)
	}
}

func TestWithLogService_SetsAndClearsTheService(t *testing.T) {
	if tr := newExtTransport(t, WithLogService(nil)); tr.logSvc != nil {
		t.Fatal("WithLogService(nil) left a service")
	}
}

// failingLogService answers every LogService call with err.
type failingLogService struct{ err error }

var _ service.LogService = failingLogService{}

func (f failingLogService) CreateLog(context.Context, *service.CreateLogRequest) (*service.CreateLogResponse, error) {
	return nil, f.err
}

func (f failingLogService) ListLogs(context.Context, *service.ListLogsRequest) (*service.ListLogsResponse, error) {
	return nil, f.err
}

func (f failingLogService) Append(context.Context, *service.AppendRequest) (*service.AppendResponse, error) {
	return nil, f.err
}

func (f failingLogService) GetEntries(context.Context, *service.GetEntriesRequest) (*service.GetEntriesResponse, error) {
	return nil, f.err
}

func (f failingLogService) ProveInclusion(context.Context, *service.InclusionRequest) (*service.InclusionResponse, error) {
	return nil, f.err
}

func (f failingLogService) ProveConsistency(context.Context, *service.ConsistencyRequest) (*service.ConsistencyResponse, error) {
	return nil, f.err
}

func (f failingLogService) Head(context.Context, *service.HeadRequest) (*service.HeadResponse, error) {
	return nil, f.err
}

func (f failingLogService) SignHead(context.Context, *service.SignHeadRequest) (*service.SignHeadResponse, error) {
	return nil, f.err
}

func (f failingLogService) RotateKey(context.Context, *service.RotateKeyRequest) (*service.RotateKeyResponse, error) {
	return nil, f.err
}

func (f failingLogService) ExpireBodies(context.Context, *service.ExpireBodiesRequest) (*service.ExpireBodiesResponse, error) {
	return nil, f.err
}

func TestVlog_ServiceFailureIsReturnedFromEveryMethod(t *testing.T) {
	cause := errors.New("log service down")
	tr := newExtTransport(t, WithLogService(failingLogService{err: cause}))
	errs := vlogCalls(context.Background(), tr, false)
	_, err := tr.VlogList(context.Background(), nil)
	errs["VlogList"] = err
	for method, err := range errs {
		if !errors.Is(err, cause) {
			t.Errorf("%s: err = %v, want it to wrap the service's error", method, err)
		}
	}
}
