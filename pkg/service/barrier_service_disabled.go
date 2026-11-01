package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// sealDisabledRootKeyPath is where the seal-disabled barrier's root-key record
// lives in its private in-memory backend. Nothing else reads that backend.
const sealDisabledRootKeyPath = "seal-disabled/root-key"

// sealDisabledBarrierService is the barrier service of a node whose seal
// subsystem is disabled. Barrier protection "none" is a valid barrier, not
// an unconfigured one, so the node serves the barrier verbs like any other:
// the status reads from a StorageBarrier over seal.StrategyNone, initialized
// and unsealed, which is what protection none is. That barrier guards
// nothing -- the node's data never passes through it -- so every verb that
// would change it is refused with *SealDisabledError rather than delegated:
// a seal would report a sealed node while its data stayed readable.
type sealDisabledBarrierService struct {
	*BarrierService
}

// Compile-time interface check.
var _ BarrierServicer = (*sealDisabledBarrierService)(nil)

// NewSealDisabledBarrierService returns the BarrierServicer of a node whose
// seal subsystem is disabled (seal.enabled: false). Its BarrierStatus reports
// strategy none, initialized and unsealed; BarrierInitialized reports true;
// BarrierHasRecoveryKeys reports false and BarrierOfficerList no holders.
// Every other verb returns
// *SealDisabledError.
//
// The barrier logs nowhere: it guards nothing, and its strategy-none and
// initialization lines would describe a barrier the node does not have. The
// node's one account of running unencrypted is lifecycle's "seal disabled"
// WARN.
func NewSealDisabledBarrierService(ctx context.Context) (BarrierServicer, error) {
	barrier, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: sealDisabledRootKeyPath}, seal.NewNoneStrategy())
	if err != nil {
		return nil, err
	}
	if err := barrier.Initialize(ctx, seal.Credentials{}); err != nil {
		_ = barrier.Close()
		return nil, err
	}
	return &sealDisabledBarrierService{BarrierService: &BarrierService{barrier: barrier}}, nil
}

// BarrierStatus reports protection none, unsealed. InitializedAt stays zero:
// the barrier was never initialized by anyone, it is the node's
// configuration.
func (s *sealDisabledBarrierService) BarrierStatus(ctx context.Context) (*BarrierStatusInfo, error) {
	status, err := s.BarrierService.BarrierStatus(ctx)
	if err != nil {
		return nil, err
	}
	status.InitializedAt = time.Time{}
	return status, nil
}

// BarrierOfficerList reports no holders: a barrier of protection none has
// no share domain to hold.
func (s *sealDisabledBarrierService) BarrierOfficerList(context.Context, seal.QuorumDomain) ([]*BarrierOfficerStatus, error) {
	return []*BarrierOfficerStatus{}, nil
}

// BarrierInitialize implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierInitialize(context.Context, []byte) error {
	return &SealDisabledError{Op: "barrier initialize"}
}

// BarrierUnseal implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierUnseal(context.Context, []byte) error {
	return &SealDisabledError{Op: "barrier unseal"}
}

// BarrierChangePassphrase implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierChangePassphrase(context.Context, []byte, []byte) error {
	return &SealDisabledError{Op: "barrier change passphrase"}
}

// BarrierSeal implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierSeal(context.Context) error {
	return &SealDisabledError{Op: "barrier seal"}
}

// BarrierClusterUnseal implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierClusterUnseal(context.Context) (*clusterseal.Report, error) {
	return nil, &SealDisabledError{Op: "barrier cluster unseal"}
}

// BarrierInitializeShamir implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierInitializeShamir(context.Context, int, int, seal.FactorPolicy) (*ShamirInitInfo, error) {
	return nil, &SealDisabledError{Op: "barrier initialize shamir"}
}

// BarrierGenerateRecoveryKeys implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierGenerateRecoveryKeys(context.Context, int, seal.FactorPolicy) (*RecoveryKeysInfo, error) {
	return nil, &SealDisabledError{Op: "barrier generate recovery keys"}
}

// BarrierDeleteRecoveryKeys implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierDeleteRecoveryKeys(context.Context) error {
	return &SealDisabledError{Op: "barrier delete recovery keys"}
}

// BarrierOfficerRegister implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierOfficerRegister(context.Context, seal.QuorumDomain, *BarrierOfficerRegisterRequest) (*BarrierOfficerRegisterResult, error) {
	return nil, &SealDisabledError{Op: "barrier officer register"}
}

// BarrierShareClaimBegin implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierShareClaimBegin(context.Context, seal.QuorumDomain, string, string) (*BarrierShareClaimChallenge, error) {
	return nil, &SealDisabledError{Op: "barrier share claim"}
}

// BarrierShareClaimComplete implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierShareClaimComplete(context.Context, seal.QuorumDomain, string, string, []byte, string) (*BarrierShareClaimResult, error) {
	return nil, &SealDisabledError{Op: "barrier share claim"}
}

// BarrierQuorumBegin implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierQuorumBegin(context.Context, *QuorumBeginRequest) (*QuorumChallengeInfo, error) {
	return nil, &SealDisabledError{Op: "barrier quorum"}
}

// BarrierQuorumSubmit implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierQuorumSubmit(context.Context, *QuorumSubmitRequest) (*QuorumOutcomeInfo, error) {
	return nil, &SealDisabledError{Op: "barrier quorum"}
}

// BarrierRekeyStart implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierRekeyStart(context.Context, seal.QuorumDomain, int, seal.FactorPolicy, string) (*QuorumRoundInfo, error) {
	return nil, &SealDisabledError{Op: "barrier rekey"}
}

// BarrierOfficerRevoke implements BarrierServicer: refused.
func (s *sealDisabledBarrierService) BarrierOfficerRevoke(context.Context, seal.QuorumDomain, string, int, string) (*QuorumRoundInfo, error) {
	return nil, &SealDisabledError{Op: "barrier officer revoke"}
}
