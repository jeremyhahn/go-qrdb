package embedded

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newSealStoreTransport is an embedded transport over a real seal store on
// an initialized software-strategy barrier with a memory backend.
func newSealStoreTransport(t *testing.T) *Transport {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)
	s, err := seal.NewSoftwareStrategy(ctx, []byte("test-master-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "test/root-key"}, s)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(ctx, seal.MustCredentials(ctx, []byte("test-password"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ss, err := seal.NewSealStore(b, seal.SealStoreDefaultPrefix, logger)
	if err != nil {
		t.Fatalf("NewSealStore: %v", err)
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(newScopeRegistry()), WithSealStore(ss))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

// sealStoreRoundTrip drives every seal-store verb through tr.
func sealStoreRoundTrip(t *testing.T, tr *Transport) {
	t.Helper()
	ctx := context.Background()
	if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "alpha", Secret: []byte("s3cret")}); err != nil {
		t.Fatalf("SealStorePut: %v", err)
	}
	got, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
	if err != nil {
		t.Fatalf("SealStoreGet: %v", err)
	}
	if got.Name != "alpha" || string(got.Secret) != "s3cret" {
		t.Fatalf("SealStoreGet = %q/%q, want alpha/s3cret", got.Name, got.Secret)
	}
	list, err := tr.SealStoreList(ctx)
	if err != nil {
		t.Fatalf("SealStoreList: %v", err)
	}
	if len(list.Names) != 1 || list.Names[0] != "alpha" {
		t.Fatalf("SealStoreList = %v, want [alpha]", list.Names)
	}
	status, err := tr.SealStoreStatus(ctx)
	if err != nil {
		t.Fatalf("SealStoreStatus: %v", err)
	}
	if !status.Available || status.SecretCount != 1 {
		t.Fatalf("SealStoreStatus = %+v, want available with 1 secret", status)
	}
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "alpha"}); err != nil {
		t.Fatalf("SealStoreReseal: %v", err)
	}
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "alpha"}); err != nil {
		t.Fatalf("SealStoreDelete: %v", err)
	}
	if _, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"}); err == nil {
		t.Fatal("SealStoreGet after delete: want an error")
	}
}

func TestEmbeddedSealStore_RoundTrip(t *testing.T) {
	sealStoreRoundTrip(t, newSealStoreTransport(t))
}

func TestEmbeddedSealStore_ScopedCloneRoundTrip(t *testing.T) {
	root := newSealStoreTransport(t)
	st, err := root.WithDatabase("example")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	sealStoreRoundTrip(t, st.(*Transport))
}

func TestEmbeddedSealStore_ServiceErrorsPropagate(t *testing.T) {
	tr := newSealStoreTransport(t)
	ctx := context.Background()
	if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: ""}); !errors.Is(err, seal.ErrInvalidSecretName) {
		t.Errorf("SealStorePut: err = %v, want ErrInvalidSecretName", err)
	}
	if _, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: ""}); !errors.Is(err, seal.ErrInvalidSecretName) {
		t.Errorf("SealStoreGet: err = %v, want ErrInvalidSecretName", err)
	}
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: ""}); !errors.Is(err, seal.ErrInvalidSecretName) {
		t.Errorf("SealStoreDelete: err = %v, want ErrInvalidSecretName", err)
	}
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: ""}); !errors.Is(err, seal.ErrInvalidSecretName) {
		t.Errorf("SealStoreReseal: err = %v, want ErrInvalidSecretName", err)
	}
}

func TestEmbeddedSealStore_NilServiceRefused(t *testing.T) {
	root, _ := newScopedPair(t)
	ctx := context.Background()
	var nilSvc *NilServiceError
	errs := []error{
		root.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "a"}),
		root.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "a"}),
		root.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "a"}),
	}
	_, err := root.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "a"})
	errs = append(errs, err)
	_, err = root.SealStoreList(ctx)
	errs = append(errs, err)
	_, err = root.SealStoreStatus(ctx)
	errs = append(errs, err)
	for i, err := range errs {
		if !errors.As(err, &nilSvc) {
			t.Errorf("verb %d: err = %v, want *NilServiceError", i, err)
		}
	}
}

func TestEmbeddedSealStore_RootCloseRefusesAdmittedVerbs(t *testing.T) {
	root := newSealStoreTransport(t)
	st, err := root.WithDatabase("example")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()
	for _, tr := range []*Transport{root, st.(*Transport)} {
		errs := []error{
			tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "a", Secret: []byte("x")}),
			tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "a"}),
			tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "a"}),
		}
		_, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "a"})
		errs = append(errs, err)
		_, err = tr.SealStoreList(ctx)
		errs = append(errs, err)
		_, err = tr.SealStoreStatus(ctx)
		errs = append(errs, err)
		var closed *transport.ClosedError
		for i, err := range errs {
			if !errors.As(err, &closed) {
				t.Errorf("verb %d: err = %v, want *transport.ClosedError", i, err)
			}
		}
	}
}
