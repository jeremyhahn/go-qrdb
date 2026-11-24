package grpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// authRecorder records the "authorization" metadata of every unary RPC.
type authRecorder struct {
	mu      sync.Mutex
	seen    []string
	streams chan string
}

// streamIntercept records the authorization metadata of each streaming RPC before its handler runs.
func (r *authRecorder) streamIntercept(srv any, ss googlegrpc.ServerStream, _ *googlegrpc.StreamServerInfo, handler googlegrpc.StreamHandler) error {
	md, _ := metadata.FromIncomingContext(ss.Context())
	select {
	case r.streams <- fmt.Sprint(md.Get(transport.AuthorizationMetadataKey)):
	default:
	}
	return handler(srv, ss)
}

func (r *authRecorder) intercept(ctx context.Context, req any, _ *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	r.mu.Lock()
	r.seen = append(r.seen, fmt.Sprint(md.Get(transport.AuthorizationMetadataKey)))
	r.mu.Unlock()
	return handler(ctx, req)
}

func (r *authRecorder) last() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		return "", 0
	}
	return r.seen[len(r.seen)-1], len(r.seen)
}

// bearerHealthyServer answers Health so Connect succeeds; every other RPC is
// unimplemented, which is enough: the recorder sees the metadata first.
type bearerHealthyServer struct {
	pb.UnimplementedQRDBServiceServer
}

func (bearerHealthyServer) Health(context.Context, *emptypb.Empty) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Status: "ok"}, nil
}

type grpcCountingSource struct{ n atomic.Int64 }

func (s *grpcCountingSource) Token(context.Context) (string, error) {
	return fmt.Sprintf("tok-%d", s.n.Add(1)), nil
}

func bearerGRPCServer(t *testing.T, secure bool) (*authRecorder, string) {
	t.Helper()
	rec := &authRecorder{streams: make(chan string, 8)}
	opts := []googlegrpc.ServerOption{
		googlegrpc.UnaryInterceptor(rec.intercept),
		googlegrpc.StreamInterceptor(rec.streamIntercept),
	}
	if secure {
		opts = append(opts, googlegrpc.Creds(credentials.NewTLS(sharedGRPCServerTLS)))
	}
	srv := googlegrpc.NewServer(opts...)
	pb.RegisterQRDBServiceServer(srv, bearerHealthyServer{})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return rec, lis.Addr().String()
}

func connectBearerGRPC(t *testing.T, addr string, opts ...transport.Option) *Transport {
	t.Helper()
	tr, err := New(append([]transport.Option{transport.WithAddress(addr)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestBearer_GRPCSendsAuthorizationMetadata(t *testing.T) {
	rec, addr := bearerGRPCServer(t, true)
	tr := connectBearerGRPC(t, addr, sharedGRPCClientTLSOption(), transport.WithBearerToken("host-issued-token"))

	_, _ = tr.Status(context.Background())
	if got, n := rec.last(); n == 0 || got != "[Bearer host-issued-token]" {
		t.Fatalf("authorization = %s (%d RPCs), want [Bearer host-issued-token]", got, n)
	}

	clone, err := tr.WithDatabase("other")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	_, before := rec.last()
	admin, ok := clone.(transport.AdminClient)
	if !ok {
		t.Fatalf("WithDatabase clone %T is not an AdminClient", clone)
	}
	_, _ = admin.Status(context.Background())
	if got, n := rec.last(); n == before || got != "[Bearer host-issued-token]" {
		t.Fatalf("WithDatabase clone: authorization = %s, want [Bearer host-issued-token]", got)
	}
}

// requireGRPCWatchBearer opens a Watch stream on w and requires the server to see the bearer token.
func requireGRPCWatchBearer(t *testing.T, rec *authRecorder, w transport.WatchClient, label string) {
	t.Helper()
	for len(rec.streams) > 0 {
		<-rec.streams
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = w.Watch(ctx, "k", transport.WatchOptions{})
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case got := <-rec.streams:
		if got != "[Bearer host-issued-token]" {
			t.Fatalf("%s: watch stream authorization = %s, want [Bearer host-issued-token]", label, got)
		}
	case <-timer.C:
		t.Fatalf("%s: server saw no watch stream", label)
	}
}

func TestBearer_GRPCWatchStreamSendsAuthorizationMetadata(t *testing.T) {
	rec, addr := bearerGRPCServer(t, true)
	tr := connectBearerGRPC(t, addr, sharedGRPCClientTLSOption(), transport.WithBearerToken("host-issued-token"))
	requireGRPCWatchBearer(t, rec, tr, "transport")

	clone, err := tr.WithDatabase("other")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	w, ok := clone.(transport.WatchClient)
	if !ok {
		t.Fatalf("WithDatabase clone %T is not a WatchClient", clone)
	}
	requireGRPCWatchBearer(t, rec, w, "WithDatabase clone")
}

func TestBearer_GRPCRefreshesTokenPerRPC(t *testing.T) {
	rec, addr := bearerGRPCServer(t, true)
	src := &grpcCountingSource{}
	tr := connectBearerGRPC(t, addr, sharedGRPCClientTLSOption(), transport.WithTokenSource(src))

	_, _ = tr.Status(context.Background())
	first, _ := rec.last()
	_, _ = tr.Status(context.Background())
	second, _ := rec.last()
	if first == second {
		t.Fatalf("authorization %s repeated, want a fresh token per RPC", first)
	}
}

func TestBearer_GRPCRefusesPlaintext(t *testing.T) {
	rec, addr := bearerGRPCServer(t, false)
	tr, err := New(transport.WithAddress(addr), transport.WithBearerToken("host-issued-token"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	// Connect's health probe is the first RPC: it is refused, not sent.
	err = tr.Connect(context.Background())
	var tlsErr *transport.BearerRequiresTLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("err = %v, want *transport.BearerRequiresTLSError", err)
	}
	if got, n := rec.last(); n != 0 && got != "[]" {
		t.Fatalf("plaintext server received authorization %s", got)
	}
}
