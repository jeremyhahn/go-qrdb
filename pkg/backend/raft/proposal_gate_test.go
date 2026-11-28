package raft

import (
	"errors"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
)

// The drain waits for a proposal admitted before it, refuses one submitted
// after it as shard paused, and the reopen admits proposals again.
func TestProposalGate_DrainWaitsForAdmittedAndRefusesNew(t *testing.T) {
	h := &quicraft.Host{}
	release, err := admitProposal(h)
	if err != nil {
		t.Fatalf("admit before the seal: %v", err)
	}
	drained := make(chan error, 1)
	go func() { drained <- DrainProposals(h, time.Minute) }()
	select {
	case err := <-drained:
		t.Fatalf("DrainProposals returned (%v) with a proposal still admitted", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := admitProposal(h); !errors.Is(err, quicraft.ErrShardPaused) || !errors.Is(err, ErrProposalsSealing) {
		t.Fatalf("admit during the drain = %v, want ErrProposalsSealing wrapping quicraft.ErrShardPaused", err)
	}
	release()
	if err := <-drained; err != nil {
		t.Fatalf("DrainProposals after the release: %v", err)
	}
	ReopenProposals(h)
	release, err = admitProposal(h)
	if err != nil {
		t.Fatalf("admit after the reopen: %v", err)
	}
	release()
}

// A drain whose bound runs out names the proposals still in flight, and
// leaves the gate closed.
func TestProposalGate_DrainTimeoutNamesProposals(t *testing.T) {
	h := &quicraft.Host{}
	release, err := admitProposal(h)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	defer release()
	var timeout *quiesce.DrainTimeoutError
	if err := DrainProposals(h, 10*time.Millisecond); !errors.As(err, &timeout) {
		t.Fatalf("DrainProposals = %v, want a *quiesce.DrainTimeoutError", err)
	}
	if _, err := admitProposal(h); !errors.Is(err, ErrProposalsSealing) {
		t.Fatalf("admit after the timed-out drain = %v, want ErrProposalsSealing", err)
	}
	ReopenProposals(h)
}

// Hosts have separate gates, and a nil host admits every proposal.
func TestProposalGate_PerHostAndNilHost(t *testing.T) {
	a, b := &quicraft.Host{}, &quicraft.Host{}
	if err := DrainProposals(a, time.Second); err != nil {
		t.Fatalf("DrainProposals(a): %v", err)
	}
	defer ReopenProposals(a)
	release, err := admitProposal(b)
	if err != nil {
		t.Fatalf("admit on b while a is sealing: %v", err)
	}
	release()
	if err := DrainProposals(nil, time.Second); err != nil {
		t.Fatalf("DrainProposals(nil): %v", err)
	}
	release, err = admitProposal(nil)
	if err != nil {
		t.Fatalf("admit on nil host: %v", err)
	}
	release()
}
