package transport

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestRefusalError_SealStoreSentinels proves a remote client rebuilds the
// seal-store sentinel from each structured refusal reason (#1647), so
// errors.Is holds over the wire as it does on the embedded transport.
func TestRefusalError_SealStoreSentinels(t *testing.T) {
	cases := map[string]error{
		ReasonSealStoreSecretNotFound:    seal.ErrSealStoreSecretNotFound,
		ReasonSealStoreInvalidSecretName: seal.ErrInvalidSecretName,
		ReasonSealStoreBarrierSealed:     seal.ErrSealStoreBarrierSealed,
	}
	for reason, sentinel := range cases {
		got := RefusalError(reason, nil)
		if !errors.Is(got, sentinel) {
			t.Fatalf("RefusalError(%q, nil) = %v, want errors.Is %v", reason, got, sentinel)
		}
		wrapped := &seal.SealStoreError{Op: "get", Key: "db-password", Err: sentinel}
		if r, ok := BarrierRefusal(wrapped); !ok || r != reason {
			t.Fatalf("BarrierRefusal(%v) = %q, %v; want %q, true", wrapped, r, ok, reason)
		}
	}
}
