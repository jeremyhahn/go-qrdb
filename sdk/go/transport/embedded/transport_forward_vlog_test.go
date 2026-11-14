package embedded

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// TestForwardingScope_NamespaceAndVlogVerbsRunLocallyFirst: the namespace
// update forwards a replica refusal; the vlog verbs, with no vlog service
// on this node, hand back the local error untouched.
func TestForwardingScope_NamespaceAndVlogVerbsRunLocallyFirst(t *testing.T) {
	fwd := &recordingForwarder{status: 200}
	fs := allFamiliesForwardingScope(t, fwd)
	ctx := acmeCtx()

	before := len(fwd.calls)
	_, _ = fs.UpdateNamespace(ctx, &transport.UpdateNamespaceRequest{Namespace: "ns"})
	if len(fwd.calls) != before+1 {
		t.Fatalf("UpdateNamespace: forwarder reached %d time(s), want 1", len(fwd.calls)-before)
	}

	calls := map[string]func() error{
		"VlogAppend":       func() error { _, err := fs.VlogAppend(ctx, &vlogwire.AppendRequest{}); return err },
		"VlogCreate":       func() error { _, err := fs.VlogCreate(ctx, &vlogwire.CreateRequest{}); return err },
		"VlogExpireBodies": func() error { _, err := fs.VlogExpireBodies(ctx, &vlogwire.ExpireRequest{}); return err },
		"VlogGet":          func() error { _, err := fs.VlogGet(ctx, &vlogwire.GetRequest{}); return err },
		"VlogHead":         func() error { _, err := fs.VlogHead(ctx, &vlogwire.LogRequest{}); return err },
		"VlogList":         func() error { _, err := fs.VlogList(ctx, &vlogwire.ListRequest{}); return err },
		"VlogProveConsistency": func() error {
			_, err := fs.VlogProveConsistency(ctx, &vlogwire.ConsistencyRequest{})
			return err
		},
		"VlogProveInclusion": func() error { _, err := fs.VlogProveInclusion(ctx, &vlogwire.InclusionRequest{}); return err },
		"VlogRotateKey":      func() error { _, err := fs.VlogRotateKey(ctx, &vlogwire.RotateKeyRequest{}); return err },
		"VlogSignHead":       func() error { _, err := fs.VlogSignHead(ctx, &vlogwire.LogRequest{}); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s with no vlog service: expected an error", name)
		}
	}
}
