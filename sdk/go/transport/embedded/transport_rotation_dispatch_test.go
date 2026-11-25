package embedded

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// failingCredentialService answers the rotation and conflict reads with
// errCredential, as a credential service whose store is unreadable does.
type failingCredentialService struct {
	mockCredentialService
}

func (f *failingCredentialService) CredentialStrategy(context.Context) (string, error) {
	return "", errCredential
}

func (f *failingCredentialService) CredentialMigrationConflicts(context.Context) ([]credentials.CredentialMigrationConflictError, error) {
	return nil, errCredential
}

func TestEmbeddedCredential_Dispatch_RotationPolicy(t *testing.T) {
	ctx := context.Background()
	var saw string
	tr := newCredentialTransport(t, &mockCredentialService{
		setPolicyFn: func(_ context.Context, name string, _ credentials.RotationPolicy) error {
			saw = name
			return nil
		},
	})

	if err := tr.Request(ctx, "CredentialSetRotationPolicy", &transport.CredentialSetRotationPolicyRequest{
		Name: "db", Enabled: true, Interval: "1h", RetainVersions: 2,
	}, nil); err != nil || saw != "db" {
		t.Fatalf("set dispatch: err=%v saw=%q", err, saw)
	}

	var resp *transport.CredentialRotationPolicyResponse
	if err := tr.Request(ctx, "CredentialRotationPolicy", &transport.CredentialRotationPolicyRequest{Name: "db"}, &resp); err != nil {
		t.Fatalf("get dispatch: %v", err)
	}
	if resp == nil || resp.Name != "db" || !resp.Enabled {
		t.Fatalf("get dispatch returned %+v", resp)
	}
	if err := tr.Request(ctx, "CredentialRotationPolicy", &transport.CredentialRotationPolicyRequest{Name: "missing"}, &resp); !errors.As(err, new(*transport.KeyNotFoundError)) {
		t.Fatalf("get dispatch of a missing credential: %v", err)
	}
}

func TestEmbeddedCredential_Dispatch_RotationVerbsRefuseTheWrongRequest(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})
	for _, method := range []string{"CredentialSetRotationPolicy", "CredentialRotationPolicy", "CredentialResolveMigrationConflict"} {
		t.Run(method, func(t *testing.T) {
			err := tr.Request(context.Background(), method, "not the right type", nil)
			var invalid *InvalidRequestError
			if !errors.As(err, &invalid) || invalid.Method != method {
				t.Fatalf("expected InvalidRequestError for %s, got %T: %v", method, err, err)
			}
		})
	}
}

func TestEmbeddedCredential_ReadsSurfaceServiceErrors(t *testing.T) {
	ctx := context.Background()
	tr := newCredentialTransport(t, &failingCredentialService{})

	if _, err := tr.CredentialStrategy(ctx); !errors.Is(err, errCredential) {
		t.Errorf("CredentialStrategy: %v", err)
	}
	if err := tr.Request(ctx, "CredentialStrategy", nil, nil); !errors.Is(err, errCredential) {
		t.Errorf("CredentialStrategy dispatch: %v", err)
	}
	if _, err := tr.CredentialMigrationConflicts(ctx); !errors.Is(err, errCredential) {
		t.Errorf("CredentialMigrationConflicts: %v", err)
	}
	if err := tr.Request(ctx, "CredentialMigrationConflicts", nil, nil); !errors.Is(err, errCredential) {
		t.Errorf("CredentialMigrationConflicts dispatch: %v", err)
	}
}

// sentinelCredentialService answers the rotation-policy and migration-conflict
// verbs with err, as the credential service does for a name it does not hold.
type sentinelCredentialService struct {
	mockCredentialService
	err error
}

func (s *sentinelCredentialService) CredentialSetRotationPolicy(context.Context, string, credentials.RotationPolicy) error {
	return s.err
}

func (s *sentinelCredentialService) CredentialRotationPolicy(context.Context, string) (*service.CredentialRotationPolicyStatus, error) {
	return nil, s.err
}

func (s *sentinelCredentialService) CredentialResolveMigrationConflict(context.Context, string, credentials.MigrationResolution) error {
	return s.err
}

// TestEmbeddedCredential_RotationVerbsNotFoundIsKeyNotFound pins the embedded
// answer of the rotation-policy and migration-conflict verbs to the typed
// not-found error the wire transports return: both not-found sentinels become
// *transport.KeyNotFoundError{Key: name}, and any other error passes through
// unchanged.
func TestEmbeddedCredential_RotationVerbsNotFoundIsKeyNotFound(t *testing.T) {
	ctx := context.Background()
	verbs := map[string]func(tr *Transport) error{
		"CredentialSetRotationPolicy": func(tr *Transport) error {
			return tr.CredentialSetRotationPolicy(ctx, &transport.CredentialSetRotationPolicyRequest{Name: "missing", Enabled: true, Interval: "1h0m0s"})
		},
		"CredentialRotationPolicy": func(tr *Transport) error {
			_, err := tr.CredentialRotationPolicy(ctx, "missing")
			return err
		},
		"CredentialResolveMigrationConflict": func(tr *Transport) error {
			return tr.CredentialResolveMigrationConflict(ctx, &transport.CredentialResolveMigrationConflictRequest{Name: "missing", Resolution: string(credentials.MigrationDiscard)})
		},
	}
	sentinels := []error{credentials.ErrCredentialNotFound, credentials.ErrMigrationConflictNotFound}
	for verb, call := range verbs {
		for _, sentinel := range sentinels {
			t.Run(verb+"/"+sentinel.Error(), func(t *testing.T) {
				err := call(newCredentialTransport(t, &sentinelCredentialService{err: sentinel}))
				var knf *transport.KeyNotFoundError
				if !errors.As(err, &knf) {
					t.Fatalf("expected *transport.KeyNotFoundError, got %T (%v)", err, err)
				}
				if knf.Key != "missing" {
					t.Errorf("Key = %q, want %q", knf.Key, "missing")
				}
			})
		}
		t.Run(verb+"/other", func(t *testing.T) {
			err := call(newCredentialTransport(t, &sentinelCredentialService{err: errCredential}))
			if err != errCredential {
				t.Fatalf("err = %T (%v), want errCredential unchanged", err, err)
			}
		})
	}
}

// sentinelVerbService answers every credential delete, rotate, list-versions,
// retrieve and retrieve-version call with err.
func sentinelVerbService(err error) *sentinelCredentialService {
	return &sentinelCredentialService{
		err: err,
		mockCredentialService: mockCredentialService{
			deleteFn: func(context.Context, string) error { return err },
			rotateFn: func(context.Context, string, credentials.RotateOptions) (*credentials.RotateResult, error) {
				return nil, err
			},
			listVersionsFn: func(context.Context, string) ([]credentials.CredentialVersion, error) {
				return nil, err
			},
			retrieveFn:        func(context.Context, string) ([]byte, error) { return nil, err },
			retrieveVersionFn: func(context.Context, string, uint64) ([]byte, error) { return nil, err },
		},
	}
}

// TestEmbeddedCredential_VerbsNotFoundIsKeyNotFound pins the embedded
// not-found mapping of the remaining credential verbs: each not-found
// sentinel the service can return is *transport.KeyNotFoundError naming the
// credential, as the gRPC, QUIC, Unix and MCP transports return (the gRPC
// server maps every one of them to codes.NotFound); any other error passes
// through unchanged. The retrieve verbs return no buffer.
func TestEmbeddedCredential_VerbsNotFoundIsKeyNotFound(t *testing.T) {
	ctx := context.Background()
	// Each call reports whether it returned a non-nil buffer.
	type verb struct {
		call      func(tr *Transport) (bool, error)
		sentinels []error
	}
	notFound := []error{credentials.ErrCredentialNotFound}
	verbs := map[string]verb{
		"CredentialDelete": {func(tr *Transport) (bool, error) {
			return false, tr.CredentialDelete(ctx, "missing")
		}, notFound},
		"CredentialRotate": {func(tr *Transport) (bool, error) {
			_, err := tr.CredentialRotate(ctx, &transport.CredentialRotateRequest{Name: "missing"})
			return false, err
		}, notFound},
		"CredentialListVersions": {func(tr *Transport) (bool, error) {
			_, err := tr.CredentialListVersions(ctx, &transport.CredentialListVersionsRequest{Name: "missing"})
			return false, err
		}, notFound},
		"CredentialRetrieveBuffer": {func(tr *Transport) (bool, error) {
			buf, err := tr.CredentialRetrieveBuffer(ctx, "missing")
			return buf != nil, err
		}, notFound},
		"CredentialRetrieveVersionBuffer": {func(tr *Transport) (bool, error) {
			buf, err := tr.CredentialRetrieveVersionBuffer(ctx, &transport.CredentialRetrieveVersionRequest{Name: "missing", Version: 2})
			return buf != nil, err
		}, []error{credentials.ErrCredentialNotFound, credentials.ErrVersionNotFound, credentials.ErrVersionExpired}},
	}
	for name, v := range verbs {
		for _, sentinel := range v.sentinels {
			t.Run(name+"/"+sentinel.Error(), func(t *testing.T) {
				gotBuf, err := v.call(newCredentialTransport(t, sentinelVerbService(sentinel)))
				var knf *transport.KeyNotFoundError
				if !errors.As(err, &knf) {
					t.Fatalf("expected *transport.KeyNotFoundError, got %T (%v)", err, err)
				}
				if knf.Key != "missing" {
					t.Errorf("Key = %q, want %q", knf.Key, "missing")
				}
				if gotBuf {
					t.Errorf("expected a nil buffer")
				}
			})
		}
		t.Run(name+"/other", func(t *testing.T) {
			gotBuf, err := v.call(newCredentialTransport(t, sentinelVerbService(errCredential)))
			if err != errCredential {
				t.Fatalf("err = %T (%v), want errCredential unchanged", err, err)
			}
			if gotBuf {
				t.Errorf("expected a nil buffer")
			}
		})
	}
}

func TestEmbeddedCredential_RotationVerbsWithoutAService(t *testing.T) {
	ctx := context.Background()
	tr := newExtTransport(t)
	var nilSvc *NilServiceError
	checks := map[string]error{
		"CredentialStrategy": func() error { _, err := tr.CredentialStrategy(ctx); return err }(),
		"CredentialStore":    tr.CredentialStore(ctx, "db", []byte("v")),
		"CredentialSetRotationPolicy": tr.CredentialSetRotationPolicy(ctx, &transport.CredentialSetRotationPolicyRequest{
			Name: "db", Enabled: true, Interval: "1h", RetainVersions: 2,
		}),
		"CredentialRotationPolicy":     func() error { _, err := tr.CredentialRotationPolicy(ctx, "db"); return err }(),
		"CredentialMigrationConflicts": func() error { _, err := tr.CredentialMigrationConflicts(ctx); return err }(),
	}
	for name, err := range checks {
		if !errors.As(err, &nilSvc) {
			t.Errorf("%s: expected NilServiceError, got %T: %v", name, err, err)
		}
	}
}

func TestEmbeddedCredential_RotationVerbsWhenClosed(t *testing.T) {
	ctx := context.Background()
	tr := closedCredentialTransport(t)
	var closed *transport.ClosedError
	checks := map[string]error{
		"CredentialStrategy": func() error { _, err := tr.CredentialStrategy(ctx); return err }(),
		"CredentialStore":    tr.CredentialStore(ctx, "db", []byte("v")),
		"CredentialSetRotationPolicy": tr.CredentialSetRotationPolicy(ctx, &transport.CredentialSetRotationPolicyRequest{
			Name: "db", Enabled: true, Interval: "1h", RetainVersions: 2,
		}),
		"CredentialMigrationConflicts": func() error { _, err := tr.CredentialMigrationConflicts(ctx); return err }(),
	}
	for name, err := range checks {
		if !errors.As(err, &closed) {
			t.Errorf("%s: expected ClosedError, got %T: %v", name, err, err)
		}
	}
}

// authzCredentialTransport builds an embedded transport with a credential
// service behind the auth middleware, granting only perms.
func authzCredentialTransport(t *testing.T, perms ...permission.Permission) *Transport {
	t.Helper()
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithCredentialService(&mockCredentialService{}),
		WithAuthMiddleware(authmw.New(grantOnlyAuthz(perms...), authmw.WithRoleResolver(authtest.BuiltInRoles(t)))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

// rotationVerbCalls runs the four rotation-policy and migration-conflict verbs
// against tr and returns each verb's error.
func rotationVerbCalls(ctx context.Context, tr *Transport) map[string]error {
	return map[string]error{
		"CredentialSetRotationPolicy": tr.CredentialSetRotationPolicy(ctx, &transport.CredentialSetRotationPolicyRequest{
			Name: "db", Enabled: true, Interval: "1h", RetainVersions: 2,
		}),
		"CredentialRotationPolicy": func() error { _, err := tr.CredentialRotationPolicy(ctx, "db"); return err }(),
		"CredentialMigrationConflicts": func() error {
			_, err := tr.CredentialMigrationConflicts(ctx)
			return err
		}(),
		"CredentialResolveMigrationConflict": tr.CredentialResolveMigrationConflict(ctx,
			&transport.CredentialResolveMigrationConflictRequest{Name: "db", Resolution: "import"}),
	}
}

func TestEmbeddedCredential_RotationVerbsRequirePermission(t *testing.T) {
	ctx := ctxFor(&auth.Identity{Subject: "operator", TenantID: "acme"})

	t.Run("granted", func(t *testing.T) {
		tr := authzCredentialTransport(t, permission.CredentialsRotate, permission.CredentialsList)
		for name, err := range rotationVerbCalls(ctx, tr) {
			if err != nil {
				t.Errorf("%s with credentials:rotate and credentials:list: %v", name, err)
			}
		}
	})

	t.Run("neither", func(t *testing.T) {
		tr := authzCredentialTransport(t, permission.KVGet)
		for name, err := range rotationVerbCalls(ctx, tr) {
			if !errors.Is(err, auth.ErrUnauthorized) {
				t.Errorf("%s without credential permissions: err = %v (%T); want auth.ErrUnauthorized", name, err, err)
			}
		}
	})

	t.Run("list only", func(t *testing.T) {
		tr := authzCredentialTransport(t, permission.CredentialsList)
		for name, err := range rotationVerbCalls(ctx, tr) {
			if name == "CredentialMigrationConflicts" {
				if err != nil {
					t.Errorf("%s with credentials:list: %v", name, err)
				}
				continue
			}
			if !errors.Is(err, auth.ErrUnauthorized) {
				t.Errorf("%s without credentials:rotate: err = %v (%T); want auth.ErrUnauthorized", name, err, err)
			}
		}
	})

	t.Run("rotate only", func(t *testing.T) {
		tr := authzCredentialTransport(t, permission.CredentialsRotate)
		for name, err := range rotationVerbCalls(ctx, tr) {
			if name == "CredentialMigrationConflicts" {
				if !errors.Is(err, auth.ErrUnauthorized) {
					t.Errorf("%s without credentials:list: err = %v (%T); want auth.ErrUnauthorized", name, err, err)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s with credentials:rotate: %v", name, err)
			}
		}
	})
}

func TestEmbeddedVector_Dispatch_UpdateNamespace(t *testing.T) {
	ctx := context.Background()
	req := &transport.UpdateNamespaceRequest{Namespace: "ns", IndexConfig: map[string]string{"m": "16"}}

	tr := newExtTransport(t, WithVectorService(&mockVectorService{}))
	var resp *transport.UpdateNamespaceResponse
	if err := tr.Request(ctx, "UpdateNamespace", req, &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp == nil || resp.Namespace != "ns" || !resp.RequiresRebuild || !resp.Success {
		t.Fatalf("dispatch returned %+v", resp)
	}

	var invalid *InvalidRequestError
	if err := tr.Request(ctx, "UpdateNamespace", "not the right type", nil); !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidRequestError, got %T: %v", err, err)
	}

	if err := newExtTransport(t, WithVectorService(&errVectorService{})).Request(ctx, "UpdateNamespace", req, &resp); err == nil {
		t.Fatal("a failing vector service: expected an error")
	}
	var nilSvc *NilServiceError
	if _, err := newExtTransport(t).UpdateNamespace(ctx, req); !errors.As(err, &nilSvc) {
		t.Fatalf("no vector service: expected NilServiceError, got %T: %v", err, err)
	}
}

func TestToTransportRichQueryResponse_MapsTagsAndGroups(t *testing.T) {
	if got := mapToTags(nil); got != nil {
		t.Errorf("mapToTags(nil) = %v, want nil", got)
	}
	one, two := 1.0, 2.0
	resp := toTransportRichQueryResponse(&timeseries.QueryResult{
		Metric: "cpu",
		Tags:   map[string]string{"host": "a"},
		GroupedResults: []*timeseries.GroupedResult{
			{Tags: map[string]string{"dc": "x"}, AggregatedValue: &two},
		},
		AggregatedValue: &one,
	})
	if resp.Metric != "cpu" || len(resp.Tags) != 1 || resp.Tags[0] != (transport.TSTag{Key: "host", Value: "a"}) {
		t.Fatalf("top level = %+v", resp)
	}
	if len(resp.GroupedResults) != 1 || resp.GroupedResults[0].AggregatedValue == nil || *resp.GroupedResults[0].AggregatedValue != 2 ||
		resp.GroupedResults[0].Tags[0] != (transport.TSTag{Key: "dc", Value: "x"}) {
		t.Fatalf("groups = %+v", resp.GroupedResults)
	}
}

func TestUnregisteredOperationError_NamesTheMethod(t *testing.T) {
	err := (&UnregisteredOperationError{Method: "Nope"}).Error()
	if err == "" || !strings.Contains(err, "Nope") {
		t.Fatalf("Error() = %q", err)
	}
}

// TestDispatch_ClosedTransportSurfacesTheError: each read verb's dispatch
// handler hands back the error its method returns on a closed transport.
func TestDispatch_ClosedTransportSurfacesTheError(t *testing.T) {
	tr := closedCredentialTransport(t)
	cases := map[string]any{
		"Health":            nil,
		"Status":            nil,
		"Peers":             nil,
		"Capabilities":      nil,
		"GetTopology":       nil,
		"CurrentRevision":   nil,
		"LeaseList":         nil,
		"GetShardState":     uint64(1),
		"ReplicaStates":     uint64(1),
		"RestoreToRevision": &transport.RestoreRevisionRequest{},
	}
	for method, req := range cases {
		t.Run(method, func(t *testing.T) {
			if err := tr.Request(context.Background(), method, req, nil); err == nil {
				t.Fatalf("%s on a closed transport: expected an error", method)
			}
		})
	}
}

func TestConverters_NilInNilOut(t *testing.T) {
	if toTransportRestoreRevisionResult(nil) != nil || toTransportIndexPerformanceMetrics(nil) != nil ||
		toTransportLeaseSlice(nil) != nil || toTransportNamespaceInfoSlice(nil) != nil ||
		toTransportVectorDataPtrSlice(nil) != nil || toTransportSearchResults(nil) != nil {
		t.Fatal("a nil input converted to a non-nil output")
	}
	if got := toServiceWatchOpts(transport.WatchOptions{ProgressNotify: true}); len(got) != 1 {
		t.Fatalf("toServiceWatchOpts = %d options, want 1", len(got))
	}
	if got := toTransportWatchResponse(watch.WatchResponse{Events: []*watch.Event{nil}, Revision: 3}); len(got.Events) != 0 || got.Revision != 3 {
		t.Fatalf("toTransportWatchResponse = %+v", got)
	}
}
