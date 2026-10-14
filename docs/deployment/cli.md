# Deployment — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

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

No verbs of its own. The one flag involved is `qrdbd start
--secure-memory-budget-mib` (int64 MiB, default `0` = derive from the host),
documented with its environment and YAML forms in
[../configuration/cli.md](../configuration/cli.md#secure-memory-budget).

One environment variable is read by `qrdbd start` and by the `qrdb` desktop and
CLI binary, and is deliberately not a configuration key: `QRDB_SECURE_ALLOW_PROCESS_INSPECTION=true` leaves the
process readable by a debugger, with a warning logged at start (see
[user.md](user.md#attaching-a-debugger)). Unset is the default and the only
production setting.

Run-time settings the host supplies:

```bash
docker run --cap-add IPC_LOCK --ulimit memlock=-1:-1 <image> start --config /etc/qrdb/qrdb.yaml
ulimit -l unlimited && <install-dir>/qrdbd start --config /etc/qrdb/qrdb.yaml
```
