package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

type bearerCtxKey struct{}

// bearerProbe records the Authorization header of every HTTP request, and
// the one a tool call ran under.
type bearerProbe struct {
	mu      sync.Mutex
	headers []string
	tool    []string
}

func (p *bearerProbe) handler() http.Handler {
	srv := mcpserver.NewMCPServer("bearer-probe", "1")
	srv.AddTool(mcp.NewTool("qrdb_barrier_rekey_start"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		auth, _ := ctx.Value(bearerCtxKey{}).(string)
		p.mu.Lock()
		p.tool = append(p.tool, auth)
		p.mu.Unlock()
		return mcp.NewToolResultText(`{"domain":"unseal","op":"rekey","required":2,"submitted":0}`), nil
	})
	inner := mcpserver.NewStreamableHTTPServer(srv, mcpserver.WithHTTPContextFunc(
		func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, bearerCtxKey{}, r.Header.Get(transport.AuthorizationHeader))
		}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.headers = append(p.headers, r.Header.Get(transport.AuthorizationHeader))
		p.mu.Unlock()
		inner.ServeHTTP(w, r)
	})
}

func (p *bearerProbe) snapshot() (headers, tool []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.headers...), append([]string(nil), p.tool...)
}

type mcpCountingSource struct{ n atomic.Int64 }

func (s *mcpCountingSource) Token(context.Context) (string, error) {
	return fmt.Sprintf("tok-%d", s.n.Add(1)), nil
}

func connectBearerMCP(t *testing.T, srv *httptest.Server, opts ...transport.Option) *Transport {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	opts = append([]transport.Option{
		transport.WithAddress(srv.URL + "/mcp"),
		transport.WithTLSConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}),
	}, opts...)
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

func rekeyStart(tr *Transport) error {
	_, err := tr.BarrierRekeyStart(context.Background(), &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2})
	return err
}

func TestBearer_MCPSendsAuthorizationOnInitializeAndToolCall(t *testing.T) {
	probe := &bearerProbe{}
	srv := httptest.NewTLSServer(probe.handler())
	defer srv.Close()
	tr := connectBearerMCP(t, srv, transport.WithBearerToken("host-issued-token"))
	if err := rekeyStart(tr); err != nil {
		t.Fatalf("tool call: %v", err)
	}
	headers, tool := probe.snapshot()
	if len(headers) == 0 || len(tool) != 1 {
		t.Fatalf("server saw %d requests and %d tool calls", len(headers), len(tool))
	}
	// The first request is initialize.
	for _, h := range append(headers, tool...) {
		if h != "Bearer host-issued-token" {
			t.Fatalf("Authorization = %q, want Bearer host-issued-token (requests %q, tool %q)", h, headers, tool)
		}
	}
}

func TestBearer_MCPWithDatabaseCloneSendsAuthorization(t *testing.T) {
	probe := &bearerProbe{}
	srv := httptest.NewTLSServer(probe.handler())
	defer srv.Close()
	tr := connectBearerMCP(t, srv, transport.WithBearerToken("host-issued-token"))

	scoped, err := tr.WithDatabase("other")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	clone, ok := scoped.(*Transport)
	if !ok {
		t.Fatalf("WithDatabase clone %T is not a *Transport", scoped)
	}
	before, _ := probe.snapshot()
	if err := rekeyStart(clone); err != nil {
		t.Fatalf("tool call on clone: %v", err)
	}
	headers, tool := probe.snapshot()
	if len(headers) == len(before) || len(tool) != 1 {
		t.Fatalf("server saw %d new requests and %d tool calls from the clone", len(headers)-len(before), len(tool))
	}
	for _, h := range append(headers, tool...) {
		if h != "Bearer host-issued-token" {
			t.Fatalf("Authorization = %q, want Bearer host-issued-token (requests %q, tool %q)", h, headers, tool)
		}
	}
}

func TestBearer_MCPRefreshesTokenPerRequest(t *testing.T) {
	probe := &bearerProbe{}
	srv := httptest.NewTLSServer(probe.handler())
	defer srv.Close()
	src := &mcpCountingSource{}
	tr := connectBearerMCP(t, srv, transport.WithTokenSource(src))
	for i := 0; i < 2; i++ {
		if err := rekeyStart(tr); err != nil {
			t.Fatalf("tool call %d: %v", i, err)
		}
	}
	_, tool := probe.snapshot()
	if len(tool) != 2 || tool[0] == tool[1] {
		t.Fatalf("tool calls ran under %q, want a fresh token per request", tool)
	}
}

func TestBearer_MCPRefusesPlaintext(t *testing.T) {
	probe := &bearerProbe{}
	srv := httptest.NewServer(probe.handler())
	defer srv.Close()
	tr, err := New(transport.WithAddress(srv.URL+"/mcp"), transport.WithBearerToken("host-issued-token"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	err = tr.Connect(context.Background())
	var tlsErr *transport.BearerRequiresTLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("Connect over http: err = %v, want *transport.BearerRequiresTLSError", err)
	}
	if headers, _ := probe.snapshot(); len(headers) != 0 {
		t.Fatalf("plaintext server received %d requests", len(headers))
	}
}
