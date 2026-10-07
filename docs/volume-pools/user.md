# Volume Pools — User Guide

> **Status: APPROVED — ruled 2026-10-04 (D1-D9), 2026-10-06 (D10); Phase 1
> build in progress.** · Scope: go-qrdb · Last verified: 2026-10-06
>
> Moving a tenant between pools (`qrdb tenant move`) is **Phase 2** (decision
> D5) and not available in this build; everything else below is Phase 1.

Test reference: `make integration-test-volume-pools`; coverage row: `test/integration/COVERAGE.md` `[volume-pools](../../docs/volume-pools/)`.

A volume pool is a named place on disk where dedicated tenants live. You give a
pool a name once for the whole cluster. Each node then says, in its own
configuration, which of its directories or disks that name means on that node.
A `volume` or `raft` tenant is placed on one pool and lives at
`{that node's pool directory}/{tenant}` on every node that serves it.

## Before you start

- A cluster administrator identity in the `system` tenant.
- On every node that should serve the pool: a directory (or, with an encrypted
  provider, a block device) the qrdb daemon can write to, and that is not
  inside the node's data directory.

## 1. Create the pool

```bash
qrdb volume-pool create fast --description "NVMe tier"
```

The pool exists but is not served anywhere yet.

## 2. Map it on each node

On each node, add the pool to the daemon configuration with that node's own
directory, then restart the daemon:

```yaml
storage:
  volume_pools:
    - name: fast
      path: /srv/qrdb/fast
```

The directory can differ from node to node. That is the point: the cluster
records only the name.

Then check that every node reports the pool ready:

```bash
qrdb volume-pool get fast
```

## 3. Entitle tenants to the pool

A tenant can be placed only on pools it is entitled to. The first pool listed is
its default:

```bash
qrdb tenant set-entitlement acme --isolation-modes prefix,volume \
     --allow-volume-storage --allowed-volume-pools fast,bulk
```

A tenant's storage quota is the space it reserves on its pool (here 200 GiB):

```bash
qrdb tenant update-quotas acme --max-storage-bytes 214748364800
```

## 4. Create tenants on the pool

```bash
qrdb tenant create --id acme --name "Acme" --isolation-type volume --pool fast
```

If the pool cannot fit the tenant's reservation, the create is refused and
nothing is created. Raise the pool's capacity or choose another pool.

## Everyday tasks

- **See every pool at a glance:** `qrdb volume-pool list`.
- **See which tenants are on a pool:** `qrdb volume-pool tenants fast`.
- **Grow a pool:** grow the disk on every node, then, if you set a capacity by
  hand, `qrdb volume-pool update fast --capacity 3TiB`.
- **Move a tenant to another pool** *(Phase 2, not available in this
  build)*: `qrdb tenant move acme --to bulk --wait`. The tenant is offline
  while its data is copied, so schedule it.
- **Retire a pool:** move or delete its tenants, then
  `qrdb volume-pool delete fast`. Remove the mapping from each node's
  configuration afterwards.

## Encrypted pools

When your deployment includes go-xkms, it can provide an encrypted pool
provider (`luks2`):

```bash
qrdb volume-pool create vault --provider luks2
```

Each node maps the pool to a block device. The device unlocks automatically
whenever that node unseals, using a key your qrdb cluster derives from its
own unseal state -- nothing you manage separately, and nothing persisted
anywhere. A sealed node cannot unlock its encrypted pools; that is by
design, not a bug to work around. If your deployment also configures a
hardware key backend (TPM, HSM or cloud KMS) in go-xkms, that backend is for
break-glass recovery, not routine unlock. Tenant data is still encrypted
with the tenant's own key on top of this.

## If a node's volume pool can't open

By default, `qrdbd start` fails outright -- and the node does not start at
all -- if any pool it maps in `storage.volume_pools` can't be opened (a
missing directory, an absent device, an unregistered provider). The error
names every pool it couldn't open and why, so you fix the mount and retry:

```bash
qrdbd start --config qrdbd.yaml
# volume: 1 volume pool(s) failed to open at boot (pass
# --allow-degraded-volume-boot to start anyway, serving every volume and
# database this node can): "fast" (provider "dir"): stat /mnt/nvme-a: no
# such file or directory;
```

If you'd rather bring the node up serving everything it CAN while that one
mount is fixed -- for example, during a maintenance window on one disk --
pass `--allow-degraded-volume-boot` (or set `storage.allow_degraded_volume_boot:
true` in the node's config). The node starts, the failed pool's tenants are
refused on this node only (other nodes may still serve them), and the skip
is never silent: it's an ERROR in this node's log, visible in
`qrdb volume-pool get <pool>`, and exported as the
`qrdb_volume_pool_ready` metric. Once the mount is fixed, restart the node
without the flag to go back to the stricter default.

## Troubleshooting

| You see | Meaning | Do |
|---|---|---|
| `qrdbd start` fails immediately naming a pool | the default: a configured volume pool could not be opened | fix the mount/device, or pass `--allow-degraded-volume-boot` to start anyway (see above) |
| `volume-pool get` shows a node `unmapped` | that node's configuration does not map the pool | add the mapping and restart the node |
| a node `unavailable` | the node mapped the pool but could not open it: missing directory, not writable, device absent | fix the directory or device; the node reports again within seconds |
| a node `locked` | an encrypted pool's device is locked because that node is sealed, or its unlock failed at the last unseal | unseal (or re-unseal) the node; if it stays locked after that, check the provider's detail text for the underlying device error |
| a node `no report` | the node is down or partitioned | bring the node back |
| a tenant listed `Refused: pool_unavailable` | the node you asked cannot open the tenant's pool | look at the pool's per-node health; other nodes may still serve the tenant |
| a tenant listed `Refused: persisted_path` | the tenant was created by an older release with a fixed path | delete the tenant and re-create it on a pool |
| `create` refused with "resource exhausted" | the pool's capacity is reserved | raise capacity, use another pool, or lower the tenant's storage quota |
| `create` refused with "permission denied … volume_pool" | the tenant is not entitled to the pool | add the pool to its entitlements |
| `volume-pool delete` refused | tenants are still assigned | move or delete them first |
| `--data-root` is an unknown flag | fixed paths were removed | use `--pool` |
