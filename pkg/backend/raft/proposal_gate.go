package raft

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"weak"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
)

// ErrProposalsSealing refuses a proposal submitted once a seal has closed its
// host's proposal gate. It wraps quicraft.ErrShardPaused, so every transport
// reports it as the seal's unavailable refusal, as it does a proposal the
// paused shard refuses itself.
var ErrProposalsSealing = fmt.Errorf("%w: the at-rest barrier is sealing, proposals are refused", quicraft.ErrShardPaused)

// proposalGate counts one host's proposals from submit until their result,
// so a seal drains them before it pauses the host's shards. A proposal the
// pause catches already submitted is neither applied nor refused until the
// resume: it would end at its caller's deadline as a propose timeout.
type proposalGate struct {
	gate  quiesce.Gate
	lease *quiesce.Lease
}

// proposalGates maps each host, by a weak pointer that never keeps a closed
// host alive, to its *proposalGate. Every adapter over the same host shares
// one gate, and the seal reaches it from the host.
var proposalGates sync.Map // weak.Pointer[quicraft.Host] -> *proposalGate

// proposalGateFor returns h's gate, made on first use; nil for a nil host.
func proposalGateFor(h *quicraft.Host) *proposalGate {
	if h == nil {
		return nil
	}
	key := weak.Make(h)
	if g, ok := proposalGates.Load(key); ok {
		return g.(*proposalGate)
	}
	g := &proposalGate{}
	g.lease = g.gate.Register("raft proposals")
	actual, loaded := proposalGates.LoadOrStore(key, g)
	if !loaded {
		runtime.AddCleanup(h, func(k weak.Pointer[quicraft.Host]) { proposalGates.Delete(k) }, key)
	}
	return actual.(*proposalGate)
}

// admitProposal holds h's proposal gate for one proposal, or refuses with
// ErrProposalsSealing once a seal closed it. Every nil error must be paired
// with one call of the returned release.
func admitProposal(h *quicraft.Host) (release func(), err error) {
	g := proposalGateFor(h)
	if g == nil {
		return func() {}, nil
	}
	if err := g.lease.Acquire(); err != nil {
		if errors.Is(err, quiesce.ErrSealing) {
			return nil, ErrProposalsSealing
		}
		return nil, err
	}
	return g.lease.Release, nil
}

// DrainProposals closes h's proposal gate, so every proposal submitted after
// it is refused with ErrProposalsSealing, and waits up to timeout for the
// proposals already submitted to reach their result: each commits or fails
// on its own before the seal pauses the shards. It returns a
// *quiesce.DrainTimeoutError when timeout runs out first; the gate stays
// closed until ReopenProposals.
func DrainProposals(h *quicraft.Host, timeout time.Duration) error {
	g := proposalGateFor(h)
	if g == nil {
		return nil
	}
	g.gate.Close()
	return g.gate.Drain(timeout)
}

// ReopenProposals admits proposals on h again after the seal that closed its
// gate is undone. It is idempotent.
func ReopenProposals(h *quicraft.Host) {
	if g := proposalGateFor(h); g != nil {
		g.gate.Reopen()
	}
}
