<!--
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0

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
<script lang="ts">
  import { dbWizardStore } from "$stores/database-wizard.svelte.ts";

  let { onNext, onBack }: { onNext: () => void; onBack: () => void } =
    $props();

  interface ProtocolOption {
    readonly id: string;
    readonly label: string;
    readonly placeholder: string;
    readonly description: string;
  }

  const protocols: readonly ProtocolOption[] = [
    {
      id: "unix",
      label: "Unix Socket",
      placeholder: "/var/run/qrdb.sock",
      description:
        "gRPC over Unix domain socket. Lowest latency for local connections.",
    },
    {
      id: "grpc",
      label: "gRPC",
      placeholder: "localhost:9090",
      description:
        "gRPC over TCP with TLS. Standard for service-to-service communication.",
    },
    {
      id: "rest",
      label: "REST",
      placeholder: "https://localhost:8443",
      description: "HTTP/2 REST API with JSON. Browser-compatible transport.",
    },
    {
      id: "quic",
      label: "QUIC",
      placeholder: "localhost:9443",
      description:
        "gRPC over QUIC (UDP). Optimal for high-latency or mobile networks.",
    },
  ] as const;

  let transport: string = $state(
    dbWizardStore.connectionConfig?.transport ?? "unix",
  );
  let address: string = $state(
    dbWizardStore.connectionConfig?.address ?? "",
  );
  let caFile: string = $state(
    dbWizardStore.connectionConfig?.caFile ?? "",
  );
  let certFile: string = $state(
    dbWizardStore.connectionConfig?.certFile ?? "",
  );
  let keyFile: string = $state(
    dbWizardStore.connectionConfig?.keyFile ?? "",
  );

  /** Returns the placeholder for the currently selected protocol. */
  let currentPlaceholder = $derived(
    protocols.find((p) => p.id === transport)?.placeholder ?? "",
  );

  /** Returns the description for the currently selected protocol. */
  let currentDescription = $derived(
    protocols.find((p) => p.id === transport)?.description ?? "",
  );

  /** Validation: address must be non-empty (or uses placeholder). */
  let isValid = $derived(
    (address.length > 0) || (currentPlaceholder.length > 0),
  );

  function handleProtocolChange(id: string): void {
    transport = id;
    address = "";
  }

  function handleNext(): void {
    if (!isValid) return;
    dbWizardStore.connectionConfig = {
      transport,
      address: address || currentPlaceholder,
      caFile,
      certFile,
      keyFile,
    };
    onNext();
  }
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Remote Connection</h2>
    <p class="text-base-content/70 mt-2">
      Configure the connection to your remote QRDB server.
    </p>
  </div>

  <div class="w-full max-w-2xl space-y-6">
    <!-- Protocol selection -->
    <div class="form-control">
      <label class="label" for="db-protocol">
        <span class="label-text font-semibold">Transport Protocol</span>
      </label>
      <select
        id="db-protocol"
        class="select select-bordered w-full"
        bind:value={transport}
        onchange={(e: Event) =>
          handleProtocolChange((e.target as HTMLSelectElement).value)}
      >
        {#each protocols as proto (proto.id)}
          <option value={proto.id}>{proto.label}</option>
        {/each}
      </select>
      <label class="label" for="db-protocol">
        <span class="label-text-alt text-base-content/60">
          {currentDescription}
        </span>
      </label>
    </div>

    <!-- Server address -->
    <div class="form-control">
      <label class="label" for="db-server-address">
        <span class="label-text font-semibold">Server Address</span>
      </label>
      <input
        id="db-server-address"
        type="text"
        class="input input-bordered w-full"
        placeholder={currentPlaceholder}
        bind:value={address}
      />
    </div>

    <!-- TLS certificate files -->
    <div class="bg-base-200 rounded-lg p-4">
      <h3 class="mb-3 text-sm font-semibold">TLS Certificates (Optional)</h3>
      <div class="space-y-3">
        <div class="form-control">
          <label class="label" for="db-ca-file">
            <span class="label-text">CA Certificate</span>
          </label>
          <input
            id="db-ca-file"
            type="text"
            class="input input-bordered input-sm w-full"
            placeholder="/etc/qrdb/ca.pem"
            bind:value={caFile}
          />
        </div>
        <div class="form-control">
          <label class="label" for="db-cert-file">
            <span class="label-text">Client Certificate</span>
          </label>
          <input
            id="db-cert-file"
            type="text"
            class="input input-bordered input-sm w-full"
            placeholder="/etc/qrdb/client.pem"
            bind:value={certFile}
          />
        </div>
        <div class="form-control">
          <label class="label" for="db-key-file">
            <span class="label-text">Client Key</span>
          </label>
          <input
            id="db-key-file"
            type="text"
            class="input input-bordered input-sm w-full"
            placeholder="/etc/qrdb/client-key.pem"
            bind:value={keyFile}
          />
        </div>
      </div>
    </div>
  </div>

  <div class="flex w-full max-w-2xl justify-between">
    <button class="btn btn-ghost" onclick={onBack}>Back</button>
    <button class="btn btn-primary" disabled={!isValid} onclick={handleNext}>
      Next
    </button>
  </div>
</div>
