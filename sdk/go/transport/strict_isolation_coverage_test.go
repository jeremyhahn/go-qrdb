package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestFeatureNotActiveMetadata_RoundTrips(t *testing.T) {
	in := &feature.NotActiveError{Feature: feature.Feature("strict_tenant_isolation"), ShardID: 42}
	md := FeatureNotActiveMetadata(in)
	got := FeatureNotActiveFromMetadata(md)
	if got.Feature != in.Feature || got.ShardID != in.ShardID {
		t.Fatalf("round trip = %+v, want %+v", got, in)
	}
}

func TestFeatureNotActiveFromMetadata_MalformedShardIsClusterWide(t *testing.T) {
	for _, md := range []map[string]string{
		{"feature": "f"},
		{"feature": "f", "shard_id": "not-a-number"},
	} {
		got := FeatureNotActiveFromMetadata(md)
		if got.Feature != "f" || got.ShardID != 0 {
			t.Fatalf("FeatureNotActiveFromMetadata(%v) = %+v, want feature f shard 0", md, got)
		}
	}
}

func TestInvalidIsolationPolicyMetadata_RoundTrips(t *testing.T) {
	in := &tenant.InvalidIsolationPolicyError{Value: "loose"}
	got := InvalidIsolationPolicyFromMetadata(InvalidIsolationPolicyMetadata(in))
	if got.Value != "loose" {
		t.Fatalf("round trip value = %q, want loose", got.Value)
	}
	if got := InvalidIsolationPolicyFromMetadata(nil); got.Value != "" {
		t.Fatalf("empty metadata value = %q, want empty", got.Value)
	}
}

func TestReconcileFailure(t *testing.T) {
	if err := ReconcileFailure(nil, nil); err != nil {
		t.Fatalf("nil response: %v", err)
	}
	if err := ReconcileFailure(&ReconcileDeletedTenantsResponse{Inspected: 3}, nil); err != nil {
		t.Fatalf("nothing failed: %v", err)
	}
	cause := errors.New("disk busy")
	resp := &ReconcileDeletedTenantsResponse{Failed: []TenantResidueFailure{
		{TenantID: "acme", Op: "remove", Path: "tenants/acme", Error: "busy"},
		{TenantID: "beta", Op: "lookup", Error: "gone"},
	}}
	err := ReconcileFailure(resp, cause)
	var recErr *ReconcileDeletedTenantsError
	if !errors.As(err, &recErr) || len(recErr.Failed) != 2 {
		t.Fatalf("err = %v, want *ReconcileDeletedTenantsError naming both ids", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v does not wrap the cause", err)
	}
	msg := err.Error()
	for _, want := range []string{"2 failed", "tenant acme: remove tenants/acme: busy", "tenant beta: lookup: gone"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
}

func TestIsHTTP(t *testing.T) {
	cases := map[string]bool{
		"http://db.example.com:8080":  true,
		"HTTP://db.example.com:8080":  true,
		"https://db.example.com:8443": false,
		"db.example.com:8443":         false,
		"://bad":                      false,
		"":                            false,
	}
	for address, want := range cases {
		if got := IsHTTP(address); got != want {
			t.Errorf("IsHTTP(%q) = %v, want %v", address, got, want)
		}
	}
}

func TestBarrierSecretRedirectError_NamesStatusNeverTheSecret(t *testing.T) {
	err := &BarrierSecretRedirectError{Transport: "rest", Kind: BarrierSecretUnsealKey, Status: http.StatusPermanentRedirect}
	msg := err.Error()
	for _, want := range []string{"rest", string(BarrierSecretUnsealKey), "308", "Permanent Redirect"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q must name %q", msg, want)
		}
	}
	if strings.Contains(msg, testSecret) {
		t.Fatalf("message %q names the secret", msg)
	}
}

func TestBarrierSecretCarriers_PassphraseRequests(t *testing.T) {
	b := []byte(testSecret)
	cases := []struct {
		name string
		req  BarrierSecretCarrier
		want BarrierSecretKind
	}{
		{"BarrierChangePassphrase nil", (*BarrierChangePassphraseRequest)(nil), ""},
		{"BarrierChangePassphrase empty", &BarrierChangePassphraseRequest{}, ""},
		{"BarrierChangePassphrase current", &BarrierChangePassphraseRequest{Current: b}, BarrierSecretPassphrase},
		{"BarrierChangePassphrase next", &BarrierChangePassphraseRequest{Next: b}, BarrierSecretPassphrase},
		{"TenantBarrierSignedInitComplete nil", (*TenantBarrierSignedInitCompleteRequest)(nil), ""},
		{"TenantBarrierSignedInitComplete empty", &TenantBarrierSignedInitCompleteRequest{}, ""},
		{"TenantBarrierSignedInitComplete passphrase", &TenantBarrierSignedInitCompleteRequest{Passphrase: b}, BarrierSecretPassphrase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.barrierSecret(); got != tc.want {
				t.Fatalf("barrierSecret() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestImportChunkBytesForEncodedMessage(t *testing.T) {
	if got := ImportChunkBytesForEncodedMessage(100, 100); got != 0 {
		t.Fatalf("no room = %d, want 0", got)
	}
	if got := ImportChunkBytesForEncodedMessage(10, 20); got != 0 {
		t.Fatalf("negative room = %d, want 0", got)
	}
	if got := ImportChunkBytesForEncodedMessage(1024+16, 16); got != 768 {
		t.Fatalf("1024 usable = %d, want 768", got)
	}
}

// importRecorder is a BatchClient that records the chunked-import calls.
type importRecorder struct {
	BatchClient
	beginErr  error
	appendErr error
	opts      ImportOptions
	chunks    [][]byte
	seqs      []uint64
	aborted   bool
	committed bool
}

func (r *importRecorder) ImportBegin(_ context.Context, opts ImportOptions) (string, error) {
	r.opts = opts
	return "s1", r.beginErr
}

func (r *importRecorder) ImportAppend(_ context.Context, _ string, seq uint64, chunk []byte) error {
	if r.appendErr != nil {
		return r.appendErr
	}
	r.seqs = append(r.seqs, seq)
	r.chunks = append(r.chunks, append([]byte(nil), chunk...))
	return nil
}

func (r *importRecorder) ImportCommit(context.Context, string) (*ImportResult, error) {
	r.committed = true
	return &ImportResult{}, nil
}

func (r *importRecorder) ImportAbort(context.Context, string) error {
	r.aborted = true
	return nil
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestImportFileChunked_SplitsAndCommits(t *testing.T) {
	r := &importRecorder{}
	res, err := ImportFileChunked(context.Background(), r, "data.csv", bytes.NewReader([]byte("abcdefg")), ImportOptions{}, 3)
	if err != nil || res == nil {
		t.Fatalf("ImportFileChunked: res=%v err=%v", res, err)
	}
	if r.opts.Filename != "data.csv" || !r.committed || r.aborted {
		t.Fatalf("filename=%q committed=%v aborted=%v", r.opts.Filename, r.committed, r.aborted)
	}
	want := []string{"abc", "def", "g"}
	if len(r.chunks) != len(want) {
		t.Fatalf("chunks = %q, want %q", r.chunks, want)
	}
	for i, c := range r.chunks {
		if string(c) != want[i] || r.seqs[i] != uint64(i) {
			t.Fatalf("chunk %d = %q seq %d, want %q seq %d", i, c, r.seqs[i], want[i], i)
		}
	}
}

func TestImportFileChunked_DefaultChunkSize(t *testing.T) {
	r := &importRecorder{}
	if _, err := ImportFileChunked(context.Background(), r, "f", strings.NewReader("xy"), ImportOptions{}, 0); err != nil {
		t.Fatalf("ImportFileChunked: %v", err)
	}
	if len(r.chunks) != 1 || string(r.chunks[0]) != "xy" {
		t.Fatalf("chunks = %q, want one chunk xy", r.chunks)
	}
}

func TestImportFileChunked_BeginError(t *testing.T) {
	want := errors.New("begin refused")
	r := &importRecorder{beginErr: want}
	if _, err := ImportFileChunked(context.Background(), r, "f", strings.NewReader("x"), ImportOptions{}, 1); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if r.committed || r.aborted {
		t.Fatal("a refused begin committed or aborted a session")
	}
}

func TestImportFileChunked_AppendErrorAborts(t *testing.T) {
	want := errors.New("append refused")
	r := &importRecorder{appendErr: want}
	if _, err := ImportFileChunked(context.Background(), r, "f", strings.NewReader("xyz"), ImportOptions{}, 2); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if !r.aborted || r.committed {
		t.Fatalf("aborted=%v committed=%v, want aborted only", r.aborted, r.committed)
	}
}

func TestImportFileChunked_ReadErrorAborts(t *testing.T) {
	want := errors.New("read broke")
	r := &importRecorder{}
	_, err := ImportFileChunked(context.Background(), r, "f", failingReader{err: want}, ImportOptions{}, 2)
	var reqErr *RequestError
	if !errors.As(err, &reqErr) || reqErr.Method != "ImportFile" || !errors.Is(err, want) {
		t.Fatalf("err = %v, want *RequestError for ImportFile wrapping the read error", err)
	}
	if !r.aborted || r.committed {
		t.Fatalf("aborted=%v committed=%v, want aborted only", r.aborted, r.committed)
	}
}

var _ io.Reader = failingReader{}
