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

# Deployment — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-25

No verbs of its own. The one flag involved is `qrdbd start
--secure-memory-budget-mib` (int64 MiB, default `0` = derive from the host),
documented with its environment and YAML forms in
[../configuration/cli.md](../configuration/cli.md#secure-memory-budget).

Run-time settings the host supplies:

```bash
docker run --cap-add IPC_LOCK --ulimit memlock=-1:-1 <image> start --config /etc/qrdb/qrdb.yaml
ulimit -l unlimited && <install-dir>/qrdbd start --config /etc/qrdb/qrdb.yaml
```
