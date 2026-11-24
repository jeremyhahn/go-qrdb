package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// bearerRecorder records the Authorization header of every request, by path.
type bearerRecorder struct {
	mu       sync.Mutex
	seen     map[string][]string
	lastAuth string
	lastDB   string
}

// latest returns the Authorization and database headers of the most recent request.
func (r *bearerRecorder) latest() (auth, database string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAuth, r.lastDB
}

func (r *bearerRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		if r.seen == nil {
			r.seen = map[string][]string{}
		}
		r.seen[req.URL.Path] = append(r.seen[req.URL.Path], req.Header.Get(transport.AuthorizationHeader))
		r.lastAuth = req.Header.Get(transport.AuthorizationHeader)
		r.lastDB = req.Header.Get(transport.DatabaseHeader)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
}

func (r *bearerRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, v := range r.seen {
		out = append(out, v...)
	}
	return out
}

type bearerCountingSource struct{ n atomic.Int64 }

func (s *bearerCountingSource) Token(context.Context) (string, error) {
	return fmt.Sprintf("tok-%d", s.n.Add(1)), nil
}

func connectBearerREST(t *testing.T, srv *httptest.Server, opts ...transport.Option) *Transport {
	t.Helper()
	opts = append([]transport.Option{transport.WithAddress(srv.URL), transport.WithTLSConfig(trusting(srv))}, opts...)
	tr, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestBearer_RESTSendsAuthorizationOnDataAndAdminRequests(t *testing.T) {
	rec := &bearerRecorder{}
	srv := httptest.NewTLSServer(rec.handler())
	defer srv.Close()
	tr := connectBearerREST(t, srv, transport.WithBearerToken("host-issued-token"))

	_, _ = tr.Get(context.Background(), "k")
	_, _ = tr.Status(context.Background())

	got := rec.all()
	if len(got) < 2 {
		t.Fatalf("server saw %d requests, want a data-plane and an admin request", len(got))
	}
	for _, h := range got {
		if h != "Bearer host-issued-token" {
			t.Fatalf("Authorization = %q, want Bearer host-issued-token (all: %q)", h, got)
		}
	}
}

func TestBearer_RESTWithDatabaseCloneSendsAuthorization(t *testing.T) {
	rec := &bearerRecorder{}
	srv := httptest.NewTLSServer(rec.handler())
	defer srv.Close()
	tr := connectBearerREST(t, srv, transport.WithBearerToken("host-issued-token"))

	scoped, err := tr.WithDatabase("other")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	t.Cleanup(func() { _ = scoped.Close() })
	clone, ok := scoped.(*Transport)
	if !ok {
		t.Fatalf("WithDatabase clone %T is not a *Transport", scoped)
	}
	_, _ = clone.Get(context.Background(), "k")
	auth, database := rec.latest()
	if auth != "Bearer host-issued-token" {
		t.Fatalf("WithDatabase clone: Authorization = %q, want Bearer host-issued-token", auth)
	}
	if database != "other" {
		t.Fatalf("WithDatabase clone: %s = %q, want other", transport.DatabaseHeader, database)
	}
}

func TestBearer_RESTInitProofClientSendsAuthorization(t *testing.T) {
	rec := &bearerRecorder{}
	srv := httptest.NewTLSServer(rec.handler())
	defer srv.Close()
	tr := connectBearerREST(t, srv, transport.WithBearerToken("host-issued-token"))

	client, release := tr.initProofClient()
	defer release()
	resp, err := client.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	_ = resp.Body.Close()
	if auth, _ := rec.latest(); auth != "Bearer host-issued-token" {
		t.Fatalf("init proof client: Authorization = %q, want Bearer host-issued-token", auth)
	}
}

func TestBearer_RESTRefreshesTokenPerRequest(t *testing.T) {
	rec := &bearerRecorder{}
	srv := httptest.NewTLSServer(rec.handler())
	defer srv.Close()
	src := &bearerCountingSource{}
	tr := connectBearerREST(t, srv, transport.WithTokenSource(src))

	_, _ = tr.Status(context.Background())
	_, _ = tr.Status(context.Background())

	got := rec.all()
	if len(got) < 2 || got[len(got)-2] == got[len(got)-1] {
		t.Fatalf("Authorization headers = %q, want a fresh token per request", got)
	}
	if src.n.Load() < 2 {
		t.Fatalf("token source consulted %d times, want >= 2", src.n.Load())
	}
}

func TestBearer_RESTRefusesPlaintext(t *testing.T) {
	rec := &bearerRecorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()
	tr, err := New(transport.WithAddress(srv.URL), transport.WithBearerToken("host-issued-token"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	_ = tr.Connect(context.Background())
	_, err = tr.Status(context.Background())
	var tlsErr *transport.BearerRequiresTLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("Status over http: err = %v, want *transport.BearerRequiresTLSError", err)
	}
	for _, h := range rec.all() {
		if h != "" {
			t.Fatalf("plaintext server received Authorization %q", h)
		}
	}
}
