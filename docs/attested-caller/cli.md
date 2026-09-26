# Attested Caller — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

**There is no CLI or configuration-file surface for this feature.**

`qrdbd` has no flag, and `pkg/config`'s YAML schema has no field, to enable
the attested-caller gate. The gate needs a verifier, and the verifier is code —
go-xkms's `PeerAttestationVerifier` — that a composer injects. A setting that
named permissions to gate with no verifier behind it could only refuse every
one of them, so the gate is composed in code, beside the verifier it depends
on:

```go
gate, err := sdk.NewPeerAttestationGate(verifier, sdk.PeerAttestationPolicy{
	Permissions:    []sdk.Permission{/* the operations that need an attested peer */},
	MaxEvidenceAge: 5 * time.Minute,
})
if err != nil {
	return err // the policy does not describe a gate
}
n, err := node.NewStandaloneNode(ctx, cfg, serverOpts, node.WithPeerAttestationGate(gate))
```

A stock `qrdbd` therefore gates nothing. See [user.md](user.md) for how to tell
whether a refusal came from the gate.
