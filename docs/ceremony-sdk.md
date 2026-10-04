<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Ceremony SDK Exposure

This document describes the ceremony service as exposed through the go-qrdb
SDK: the protocol flow, the SDK client interface and transport coverage. The
requirements contract is [ceremony/](ceremony/); share holders are
[shamir-custody/](shamir-custody/).

## Protocol Flow

The initialization ceremony follows a linear state machine:

```
awaiting_init --> enrolling --> operational
```

### Single-Admin Mode (threshold < 2)

```
1. CeremonyInitialize   -- provisions PINs, issues the server certificate; operational at once
```

### M-of-N Mode (threshold >= 2)

```
1. CeremonyInitialize          -- enrols the named share holders (officers and custodians,
                                  each from a CSR), has the barrier split its unseal key
                                  to them, returns each holder's CA-issued certificate;
                                  enters enrolling
2. BarrierClient.ShareClaimBegin / ShareClaimComplete
                               -- each holder claims its sealed envelope, once, over a
                                  connection presenting its certificate
3. CeremonyStatus              -- operational once every holder has claimed
```

`CeremonySignCSR` issues a login certificate under the SO PIN. It is
`PreOperationalOnly` in the sealed-window sense: the BOOT-TIME sealed
listener allows it WITHOUT session authentication only during
`awaiting_init`/`enrolling`, while the cluster CA key still exists only as
the in-memory bootstrap key and there is no IAM plane yet to authenticate
against. Once the ceremony reaches `operational`, that unauthenticated
sealed-window path refuses it outright -- but the verb itself is not
retired: it is served normally over the node's ordinary, authenticated
API to a caller holding a valid session, the same as any other ceremony
verb. An officer who already holds a certificate still unseals with it on
every later restart instead, which needs only the CA certificate (public),
never this verb.

## SDK CeremonyClient Interface

Defined in `sdk/go/transport/client.go`:

```go
type CeremonyClient interface {
    CeremonyInitialize(ctx context.Context, req *CeremonyInitializeRequest) (*CeremonyInitializeResponse, error)
    CeremonyStatus(ctx context.Context) (*CeremonyStatusResponse, error)
    CeremonySignCSR(ctx context.Context, req *SignCSRInitRequest) (*SignCSRInitResponse, error)
}
```

| SDK Method | Service Method |
|---|---|
| CeremonyInitialize | CeremonyServicer.CeremonyInitialize |
| CeremonyStatus | CeremonyServicer.CeremonyStatus |
| CeremonySignCSR | CeremonyServicer.CeremonySignCSR |

## Transport Coverage

Every SDK transport — embedded, REST, gRPC, Unix, QUIC and MCP — implements
`CeremonyClient` with a compile-time interface assertion. The REST routes are
`POST /v1/ceremony/initialize`, `GET /v1/ceremony/status` and
`POST /v1/ceremony/sign-csr` (`pkg/api/rest/handlers_ceremony.go`).

## SDK Usage Example

```go
initResp, err := client.Ceremony().CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{
    SOPin:     soPIN,
    UserPin:   userPIN,
    Threshold: 2,
    Officers: []transport.CeremonyOfficerRequest{
        {Username: "alice", Role: transport.RoleSecurityOfficer, CSRPEM: aliceCSR},
        {Username: "carol", Role: transport.RoleCustodian, CSRPEM: carolCSR},
    },
})
if err != nil {
    return err
}
for _, h := range initResp.Holders {
    deliver(h.Username, h.CertPEM) // out of band, to each holder
}

// Each holder, over a connection presenting its own certificate:
begin, err := holderClient.Barrier().ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "alice"})
// ... sign the raw nonce bytes with alice's key ...
env, err := holderClient.Barrier().ShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{
    Username: "alice", NonceHex: begin.NonceHex, Signature: sig,
})
```
