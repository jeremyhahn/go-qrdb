package seal

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEpochRotationIsPersisted verifies that a barrier epoch rotation is
// durably recorded at RootKeyPath before it takes effect. go-quicraft
// activates a new epoch only after the configured StatePersister returns nil
// and fails closed without one, because data written under an unpersisted
// epoch is permanently undecryptable after a restart (the reloaded blob would
// still name the old epoch and derive only its DEK).
func TestEpochRotationIsPersisted(t *testing.T) {
	base := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "sys/root"}
	b, err := NewStorageBarrier(testLogger(), base, cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	ctx := context.Background()
	if err := b.Initialize(ctx, MustCredentials([]byte("correct horse battery staple"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	before := b.barrier.CurrentEpoch()
	newEpoch, err := b.barrier.Rotate()
	if err != nil {
		t.Fatalf("Rotate failed (StatePersister not wired?): %v", err)
	}
	if newEpoch <= before {
		t.Fatalf("epoch did not advance: %d -> %d", before, newEpoch)
	}

	blob, err := base.Get(ctx, cfg.RootKeyPath)
	if err != nil {
		t.Fatalf("reading persisted root key: %v", err)
	}
	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		t.Fatalf("unmarshal persisted blob: %v", err)
	}
	if sealed.Epoch != newEpoch {
		t.Fatalf("persisted epoch %d, want %d (rotation was not durable)", sealed.Epoch, newEpoch)
	}
	var found bool
	for _, e := range sealed.KnownEpochs {
		if e == newEpoch {
			found = true
		}
	}
	if !found {
		t.Fatalf("epoch %d missing from persisted KnownEpochs %v", newEpoch, sealed.KnownEpochs)
	}
}
