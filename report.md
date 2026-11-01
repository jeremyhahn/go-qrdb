# cx1317h — #1317 (closes #1200), slice 1 report

Done (signed, not pushed):
- 7302ce9c6 rebase onto main d816c57b6b as one commit (old wip bdb01323bd dropped)
- 8fad7fb87 finding 5: MCP tests expect 14 credential tools
- 1d2a1f13f finding 4: staged-value read error is not absence
- ff478c160 finding 3: staged value under its own `#staged<n>` key

Remaining:
- Finding 2: fence the scheduled commit on the claim. Plan: in rotateBarrier's
  scheduled path write current by CompareAndSwap(prevValue -> new) and, in the
  updateBarrierMeta fn, require meta.Claim.ID == sched.claim.ID, else write
  nothing and return a new typed error (ErrRotationClaimLost); when the meta is
  gone (deleted), best-effort delete this attempt's staged/retained keys. Test:
  a source that calls svc.DeleteCredential during the source call; afterwards
  current and meta are absent and RotateDue returns the typed error.
- Finding 1: tenant credential state on the replicated store, per tenant (not
  ForBarrier's node-local StorageBarrier, pkg/credentials/service.go:159); a
  multi-node/shared-store test; docs/credentials/architecture.md. Apply the
  stop rule if per-tenant barrier-key isolation needs a design decision.
- Acceptance: test-pkg for credentials, api/mcp, service, cmd/qrdb; coverage;
  go vet per tag set; make lint; ancestor check (passes now).
