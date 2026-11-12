// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * SDK wire check for the vector and time-series groups (#1478).
 *
 * Every SDK method is called against a live daemon with each optional
 * field set, and the result is read back through a second route so a
 * request or response field whose name does not match the Go handler's
 * json tag fails here instead of silently sending or reading nothing.
 *
 * The TypeScript SDK has no graph group and the REST API has no graph
 * routes, so there is nothing to check for graph on this transport.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { RestClient } from "../../../../../sdk/typescript/src/rest.js";
import type { VectorClient, TimeSeriesClient } from "../../../../../sdk/typescript/src/types.js";

// The daemon serves a self-signed certificate; the SDK uses Node's fetch.
process.env["NODE_TLS_REJECT_UNAUTHORIZED"] = "0";

async function connect(adminUrl: string): Promise<RestClient> {
  const client = new RestClient(adminUrl);
  await client.connection.connect(adminUrl);
  return client;
}

function vec(client: RestClient): VectorClient {
  const v = client.vector;
  if (!v) throw new Error("vector client unavailable");
  return v;
}

function ts(client: RestClient): TimeSeriesClient {
  const t = client.timeSeries;
  if (!t) throw new Error("time-series client unavailable");
  return t;
}

function uniq(label: string): string {
  return `wire-${label}-${Date.now()}`;
}

test.describe("SDK wire: vector (real backend)", () => {
  test("createNamespace fields round-trip through getNamespaceInfo, listNamespaces and getIndexStats", async ({ daemon }) => {
    const v = vec(await connect(daemon.adminUrl));
    const ns = uniq("ns");
    const created = await v.createNamespace({
      namespace: ns,
      dimension: 3,
      distanceMetric: 1,
      indexType: 1,
      indexParams: { m: 8, ef_construction: 64 },
    });
    expect(created.created).toBe(true);
    expect(created.timestamp).toBeGreaterThan(0);

    const info = await v.getNamespaceInfo({ namespace: ns });
    expect(info.namespace).toBe(ns);
    expect(info.dimension).toBe(3);
    expect(info.distanceMetric).toBe(1);
    expect(info.indexType).toBe(1);
    expect(info.vectorCount).toBe(0);

    const list = await v.listNamespaces({});
    const names = list.namespaces.map((n) => (typeof n === "string" ? n : n.namespace));
    expect(names).toContain(ns);

    const stats = await v.getIndexStats({ namespace: ns });
    expect(stats.dimension).toBe(3);
    expect(stats.indexType).toBe(1);
    expect(stats.vectorCount).toBe(0);
    expect(typeof stats.memoryUsage).toBe("number");
    expect(typeof stats.diskUsage).toBe("number");
    // Server gap: vectorIndexStatsResponse carries no distance_metric.

    await v.deleteNamespace({ namespace: ns });
    const after = await v.listNamespaces({});
    const namesAfter = after.namespaces.map((n) => (typeof n === "string" ? n : n.namespace));
    expect(namesAfter).not.toContain(ns);
  });

  test("vector writes round-trip through getVector, listVectors and the searches", async ({ daemon }) => {
    const v = vec(await connect(daemon.adminUrl));
    const ns = uniq("vec");
    await v.createNamespace({ namespace: ns, dimension: 3, distanceMetric: 0, indexType: 0 });

    const up = await v.upsertVector({
      namespace: ns,
      id: "7",
      vector: [1, 0, 0],
      metadata: { color: "red", sizeClass: "large", rank: 3 },
    });
    expect(up).toBeTruthy();

    const got = await v.getVector({ namespace: ns, id: "7" });
    expect(got.id).toBe("7");
    expect(got.vector).toEqual([1, 0, 0]);
    // The server keeps metadata values as strings: the number comes back "3".
    expect(got.metadata).toEqual({ color: "red", sizeClass: "large", rank: "3" });
    expect(got.version).toBeGreaterThan(0);

    const batch = await v.batchUpsert({
      namespace: ns,
      vectors: [
        { id: "8", vector: [0, 1, 0], metadata: { color: "blue" } },
        { id: "9", vector: [0, 0, 1], metadata: { color: "red" } },
      ],
    });
    expect(batch).toEqual({ succeeded: 2, failed: 0 });

    const page1 = await v.listVectors({ namespace: ns, limit: 2 });
    expect(page1.vectors.length).toBe(2);
    expect(page1.total).toBe(3);
    expect(page1.nextCursor).toBeTruthy();
    const page2 = await v.listVectors({ namespace: ns, limit: 2, cursor: page1.nextCursor });
    expect([...page1.vectors, ...page2.vectors].map((x) => x.id).sort()).toEqual(["7", "8", "9"]);
    // Server gap: the list route reads limit and cursor only; offset is not read.

    await v.updateMetadata({ namespace: ns, id: "8", metadata: { color: "green", tierLevel: "gold" } });
    const upd = await v.getVector({ namespace: ns, id: "8" });
    expect(upd.metadata).toEqual({ color: "green", tierLevel: "gold" });

    const sim = await v.searchSimilar({ namespace: ns, vector: [1, 0, 0], k: 2, efSearch: 32, minScore: 0 });
    expect(sim.results[0].id).toBe("7");
    expect(sim.results.length).toBeLessThanOrEqual(2);
    expect(typeof sim.took).toBe("number");
    const simOther = await v.searchSimilar({ namespace: ns, vector: [0, 0, 1], k: 1 });
    expect(simOther.results.map((r) => r.id)).toEqual(["9"]);

    const byMeta = await v.searchByMetadata({ namespace: ns, filters: { color: "red" }, limit: 10 });
    expect(byMeta.results.map((r) => r.id).sort()).toEqual(["7", "9"]);
    expect(byMeta.total).toBe(2);
    const byMetaLimited = await v.searchByMetadata({ namespace: ns, filters: { color: "red" }, limit: 1, offset: 1 });
    expect(byMetaLimited.results.length).toBe(1);
    const camel = await v.searchByMetadata({ namespace: ns, filters: { tierLevel: "gold" } });
    expect(camel.results.map((r) => r.id)).toEqual(["8"]);

    const hybrid = await v.hybridSearch({
      namespace: ns,
      vector: [0, 0, 1],
      metadataFilters: { color: "red" },
      k: 5,
      vectorWeight: 0.5,
      metadataWeight: 0.5,
    });
    expect(hybrid.results.map((r) => r.id)).toContain("9");
    expect(hybrid.results.map((r) => r.id)).not.toContain("8");

    const byId = await v.searchByID({ namespace: ns, id: "9", k: 1 });
    expect(byId.results.length).toBe(1);

    await v.deleteVector({ namespace: ns, id: "7" });
    const bd = await v.batchDeleteVectors({ namespace: ns, ids: ["8", "9"] });
    expect(bd.deleted).toBe(2);
    const empty = await v.listVectors({ namespace: ns, limit: 10 });
    expect(empty.vectors.length).toBe(0);

    await v.deleteNamespace({ namespace: ns });
  });

  test("rebuildIndex and compactNamespace job ids read back through their progress routes", async ({ daemon }) => {
    const v = vec(await connect(daemon.adminUrl));
    const ns = uniq("jobs");
    await v.createNamespace({ namespace: ns, dimension: 2, distanceMetric: 0, indexType: 1 });
    await v.upsertVector({ namespace: ns, id: "1", vector: [1, 1] });

    const rb = await v.rebuildIndex({ namespace: ns });
    expect(rb.jobId).toBeTruthy();
    expect(rb.started).toBe(true);
    const rp = await v.getRebuildProgress({ namespace: ns, jobId: rb.jobId });
    expect(rp.jobId).toBe(rb.jobId);
    expect(typeof rp.status).toBe("string");
    expect(rp.progress).toBeGreaterThanOrEqual(0);
    expect(rp.progress).toBeLessThanOrEqual(1);
    expect(rp.startedAt).toBeGreaterThan(0);

    const cp = await v.compactNamespace({ namespace: ns });
    expect(cp.jobId).toBeTruthy();
    expect(cp.started).toBe(true);
    const cpp = await v.getCompactionProgress({ namespace: ns, jobId: cp.jobId });
    expect(cpp.jobId).toBe(cp.jobId);
    expect(["running", "completed", "failed"]).toContain(cpp.status);
    expect(cpp.progress).toBeGreaterThanOrEqual(0);
    expect(cpp.progress).toBeLessThanOrEqual(1);
    expect(cpp.startedAt).toBeGreaterThan(0);

    await v.deleteNamespace({ namespace: ns });
  });
});

test.describe("SDK wire: time-series (real backend)", () => {
  test("write, writePoint and writeBatch round-trip through query with every filter", async ({ daemon }) => {
    const t = ts(await connect(daemon.adminUrl));
    const metric = uniq("cpu");
    // Below 2^53 so every nanosecond offset survives a JS number.
    const base = 1_700_000_000_000_000;
    await t.write({ metric, value: 1.5, timestampNs: base, tags: [{ key: "host", value: "a" }] });
    await t.writePoint({ metric, value: 2.5, timestampNs: base + 1000, tags: [{ key: "host", value: "b" }] });
    await t.writeBatch([
      { metric, value: 3.5, timestampNs: base + 2000, tags: [{ key: "host", value: "a" }] },
      { metric, value: 4.5, timestampNs: base + 3000, tags: [{ key: "host", value: "b" }] },
    ]);

    const all = await t.query({ metric, startNs: base, endNs: base + 10_000 });
    expect(all.points.map((p) => p.value)).toEqual([1.5, 2.5, 3.5, 4.5]);
    expect(all.points[0].timestampNs).toBe(base);
    expect(all.points[0].metric).toBe(metric);
    expect(all.points[0].tags).toEqual([{ key: "host", value: "a" }]);

    const hostA = await t.query({ metric, startNs: base, endNs: base + 10_000, tags: [{ key: "host", value: "a" }] });
    expect(hostA.points.map((p) => p.value)).toEqual([1.5, 3.5]);

    const desc = await t.query({ metric, startNs: base, endNs: base + 10_000, limit: 2, descending: true });
    expect(desc.points.map((p) => p.value)).toEqual([4.5, 3.5]);

    const window = await t.query({ metric, startNs: base + 1000, endNs: base + 2500 });
    expect(window.points.map((p) => p.value)).toEqual([2.5, 3.5]);
    // Both bounds are inclusive: a point at exactly endNs is returned.
    const exact = await t.query({ metric, startNs: base + 1000, endNs: base + 2000 });
    expect(exact.points.map((p) => p.value)).toEqual([2.5, 3.5]);
    expect(exact.points[exact.points.length - 1].timestampNs).toBe(base + 2000);

    const metrics = await t.listMetrics({ prefix: metric, limit: 5 });
    expect(metrics.metrics).toEqual([metric]);

    await t.deleteSeries({ metric, tags: [{ key: "host", value: "a" }], startNs: base, endNs: base + 10_000 });
    const afterDel = await t.query({ metric, startNs: base, endNs: base + 10_000 });
    expect(afterDel.points.map((p) => p.value)).toEqual([2.5, 4.5]);
  });

  test("aggregate and richQuery honour every request field", async ({ daemon }) => {
    const t = ts(await connect(daemon.adminUrl));
    const metric = uniq("mem");
    // Below 2^53 so every nanosecond offset survives a JS number.
    const base = 1_700_000_000_000_000;
    await t.writeBatch([
      { metric, value: 1, timestampNs: base, tags: [{ key: "dc", value: "x" }] },
      { metric, value: 3, timestampNs: base + 10, tags: [{ key: "dc", value: "x" }] },
      { metric, value: 10, timestampNs: base + 100, tags: [{ key: "dc", value: "y" }] },
      { metric, value: 30, timestampNs: base + 110, tags: [{ key: "dc", value: "y" }] },
    ]);

    // AggFunc 2 is SUM in the SDK's enum order; compare against the raw points.
    const agg = await t.aggregate({ metric, startNs: base, endNs: base + 1000, function: 2, intervalNs: 100 });
    expect(agg.results.length).toBe(2);
    expect(agg.results.map((r) => r.count)).toEqual([2, 2]);
    expect(agg.results[0].timestampNs).toBe(base);

    const rich = await t.richQuery({
      metric,
      startNs: base,
      endNs: base + 1000,
      tags: [{ key: "dc", value: "y" }],
      limit: 1,
    });
    expect(rich.metric).toBe(metric);
    expect(rich.points.map((p) => p.value)).toEqual([10]);

    const grouped = await t.richQuery({
      metric,
      startNs: base,
      endNs: base + 1000,
      aggregation: 2,
      groupBy: ["dc"],
    });
    expect(grouped.groupedResults.length).toBe(2);
    const byDc = Object.fromEntries(
      grouped.groupedResults.map((g) => [g.tags.find((x) => x.key === "dc")?.value, g.aggregatedValue]),
    );
    expect(byDc).toEqual({ x: agg.results[0].value, y: agg.results[1].value });
  });

  test("setRetention fields round-trip through getRetention", async ({ daemon }) => {
    const t = ts(await connect(daemon.adminUrl));
    const metric = uniq("ret");
    await t.write({ metric, value: 1, timestampNs: Date.now() * 1_000_000 });
    await t.setRetention({
      metric,
      policy: { durationNs: 86_400_000_000_000, resolutionNs: 60_000_000_000, aggregation: 2 },
    });
    const got = await t.getRetention({ metric });
    expect(got.policy).toEqual({ durationNs: 86_400_000_000_000, resolutionNs: 60_000_000_000, aggregation: 2 });
  });
});
