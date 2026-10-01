<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Deployment — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

## Give qrdbd locked memory

`qrdbd` locks the pages that hold cluster keys, credentials and PINs so they
never reach swap. Grant it enough and an unconfigured node gets its full
64 MiB secure-memory budget; grant it less and it starts with a smaller budget
sized from what the host allows, and says so in its startup log.

### Docker

```bash
docker run --cap-add IPC_LOCK --ulimit memlock=-1:-1 \
  -v /srv/qrdb:/data <image> start --config /etc/qrdb/qrdb.yaml
```

### Docker Compose

```yaml
services:
  qrdbd:
    image: <image>
    cap_add:
      - IPC_LOCK
    ulimits:
      memlock:
        soft: -1
        hard: -1
```

### systemd

```ini
[Service]
ExecStart=<install-dir>/qrdbd start --config /etc/qrdb/qrdb.yaml
LimitMEMLOCK=infinity
# or, instead of lifting the limit, grant the capability:
# AmbientCapabilities=CAP_IPC_LOCK
```

### Kubernetes

```yaml
securityContext:
  capabilities:
    add: ["IPC_LOCK"]
```

## Check what you got

```text
level=INFO msg="secure memory budget installed" budget_bytes=67108864 source=default rlimit_memlock_source="RLIMIT_MEMLOCK not enforced (CAP_IPC_LOCK)" rlimit_memlock_bytes=8388608
```

`source=default` with 67108864 bytes is the full budget. `source="derived from
RLIMIT_MEMLOCK"` means the host limit sized it; grant one of the settings above
to lift it.

## Troubleshooting

- **`cannot derive a secure memory budget ... less than the 1048576-byte
  minimum`** — the host limit is too small to run. Apply one of the settings
  above.
- **`secure memory budget N MiB rejected ... exceeds the process's
  locked-memory limit`** — you configured a budget larger than the host
  allows. Raise the limit as above, or lower `secure_memory_budget_mib`.
