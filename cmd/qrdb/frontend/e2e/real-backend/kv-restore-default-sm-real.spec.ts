// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * kv-restore-default-sm-real.spec.ts drives POST /kv/restore against the
 * SHARED real-backend daemon fixture (fixtures/daemon.ts's `daemon`), which
 * every other file in this directory also uses -- and which boots WITHOUT
 * an "--sm" flag, i.e. under the daemon's actual default state machine
 * (raft.sm_type: pebble, cmd/qrdbd/start.go's flag default). This is real,
 * valuable coverage of the error path an operator actually hits: reaching
 * for `kv restore` (or the KVExplorer GUI's Restore workflow, which calls
 * the identical endpoint) on a freshly-started, not-yet-MVCC-configured
 * node.
 *
 * PRODUCT BUG (confirmed, reported, NOT worked around here -- pkg/** is out
 * of scope for this test suite): every test below currently FAILS. The
 * response never names "raft.sm_type: mvcc"; it is instead the raw,
 * unhelpful "service: read failed: pebbledb: invalid query: unsupported
 * query type" -- confirmed by hand against a daemon spawned with this exact
 * fixture's flags (--auth-enabled=false, no "--sm"):
 *
 *   $ curl -sk -X POST https://127.0.0.1:<port>/kv/restore \
 *       -d '{"revision":1,"prefix":"eA=="}'
 *   {"error":"service: read failed: pebbledb: invalid query: unsupported query type"}
 *
 * Root cause (see test/integration/kv_restore_default_sm_cli_test.go's
 * identical finding, reproduced here independently over the real REST API
 * with --auth-enabled=false so it is NOT masked by the separate REST-routing
 * bug kv_restore_cli_test.go documents -- that one requires --auth-enabled,
 * which this fixture deliberately does not set): pkg/store.InvalidQueryError
 * (pkg/store/errors.go), returned by PebbleStateMachine.Lookup's default
 * case (pkg/store/pebble.go:450) for every MVCC-only query type
 * (raft.sm_type: pebble has no case for MVCCRevisionQuery /
 * MVCCGetAtRevisionQuery / MVCCScanChangesQuery, etc.), has NO
 * UnsupportedQueryType() bool method -- only pkg/engine.InvalidQueryError
 * (pkg/engine/errors.go, a DIFFERENT type in a different package) has one,
 * and pkg/engine is never on the call path for raft.sm_type: pebble at all.
 * So pkg/service.MVCCKVService.translateReadError's duck-typed match
 * (pkg/service/mvcc_backend_errors.go) never fires, and the actionable
 * MVCCBackendRequiredError this test asserts on is never produced. Fix:
 * give pkg/store.InvalidQueryError the identical UnsupportedQueryType()
 * method pkg/engine.InvalidQueryError already has.
 *
 * See kv-restore-mvcc-real.spec.ts for the complementary positive-path
 * coverage: an ACTUAL end-to-end restore against a real raft.sm_type: mvcc
 * daemon.
 */

import { test, expect } from "./fixtures/daemon.ts";

function key(label: string): string {
  return `e2e-restore-default-sm-${label}-${Date.now()}`;
}

test.describe("kv restore — default (non-MVCC) state machine (real backend)", () => {
  test("POST /kv/restore refuses with the actionable raft.sm_type: mvcc message", async ({
    request,
    daemon,
  }) => {
    const resp = await request.post(`${daemon.adminUrl}/kv/restore`, {
      data: { revision: 1, prefix: Buffer.from(key("prefix")).toString("base64") },
      headers: { "Content-Type": "application/json" },
    });

    expect(resp.ok()).toBe(false);
    const body = await resp.text();
    expect(body).toContain("raft.sm_type: mvcc");
  });

  test("GET /kv/revision (an MVCC-only query, used to compute restore target revisions) also refuses actionably", async ({
    request,
    daemon,
  }) => {
    // kv restore's whole request flow (CLI, GUI, and any scripted caller)
    // starts by reading the current revision to know what "before" means;
    // if THIS also fails silently-wrong instead of actionably, an operator
    // cannot even form a sensible restore request on this backend.
    const resp = await request.get(`${daemon.adminUrl}/kv/revision`);

    expect(resp.ok()).toBe(false);
    const body = await resp.text();
    expect(body).toContain("raft.sm_type: mvcc");
  });
});
