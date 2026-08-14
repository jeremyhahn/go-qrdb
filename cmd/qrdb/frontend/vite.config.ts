// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // The app entry chunk is split from third-party deps (manualChunks below).
    // The remaining vendor chunk is inherent dependency size; this is a Wails
    // desktop app (assets loaded from disk, not over the network), so the
    // default 500 kB network-oriented warning does not apply to the vendor
    // chunk. App code is kept separate so regressions in our own bundle size
    // remain visible under the default threshold.
    chunkSizeWarningLimit: 900,
    rollupOptions: {
      output: {
        // Split third-party dependencies into a separate vendor chunk so the
        // app entry chunk stays well under Vite's 500 kB warning threshold.
        manualChunks(id) {
          if (id.includes("node_modules")) {
            return "vendor";
          }
        },
      },
    },
  },
  resolve: {
    alias: {
      $lib: "/src/lib",
      $stores: "/src/stores",
      $components: "/src/components",
    },
  },
  server: {
    port: 5173,
    strictPort: false,
    // The API server the dev/E2E proxy forwards to. Defaults to the
    // conventional local admin port; the real-backend DOM test tier overrides
    // it via QRDB_PROXY_TARGET so each run can point at an ephemeral daemon.
    proxy: buildApiProxy(process.env.QRDB_PROXY_TARGET ?? "https://localhost:63002"),
  },
});

/**
 * buildApiProxy maps every server-side API route prefix to the given backend
 * target. `secure: false` accepts the daemon's self-signed TLS certificate.
 */
function buildApiProxy(target: string): Record<string, { target: string; secure: boolean }> {
  const prefixes = [
    "/health", "/status", "/peers", "/capabilities", "/kv", "/ts", "/tx",
    "/batch", "/watch", "/lease", "/index", "/vector", "/certs", "/sql",
    "/snapshot", "/drain", "/membership", "/v1", "/users", "/roles",
    "/permissions", "/tenants", "/audit", "/barrier", "/ceremony",
    "/authfactor", "/databases", "/seal", "/collection", "/cluster",
    "/rebalance", "/backup", "/metrics", "/admin",
  ];
  const proxy: Record<string, { target: string; secure: boolean }> = {};
  for (const p of prefixes) {
    proxy[p] = { target, secure: false };
  }
  return proxy;
}
