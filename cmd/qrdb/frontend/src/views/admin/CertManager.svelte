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
  import type {
    Client,
    CertRecord,
    EnrolledCert,
    IssuedCertWithKey,
    ExportCertResponse,
    ListCertsResponse,
  } from "@qrdb/sdk";
  import { isDesktop } from "@qrdb/sdk";
  import { onMount } from "svelte";
  import { RefreshCw } from "lucide-svelte";
  import { showToast } from "$stores/toast.svelte.ts";
  import { identityStore } from "$stores/identity.svelte.ts";
  import PageHeader from "../../components/PageHeader.svelte";
  import TenantSelect from "../../components/admin/TenantSelect.svelte";

  let { client }: { client: Client } = $props();

  // ---------------------------------------------------------------------------
  // Types
  // ---------------------------------------------------------------------------

  type ActionTab = "issue" | "enroll" | "export" | "revoke";

  type CertStatus = "Active" | "Expiring Soon" | "Revoked";

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  /** Tenant filter — defaults to the authenticated user's tenant. */
  let filterTenantId: string = $state(identityStore.tenantId || "");

  /** Certificate list state. */
  let certs: CertRecord[] = $state([]);
  let certsLoading: boolean = $state(true);

  /** Action panel visibility and active tab. */
  let actionPanelOpen: boolean = $state(false);
  let activeTab: ActionTab = $state("issue");

  /** Whether the desktop (Wails) runtime is available for local key generation. */
  const desktopAvailable = isDesktop();

  /** Issue form state. The account named by username must already exist. */
  let issueUsername: string = $state("");
  /** Pasted CSR PEM. Empty (desktop only) generates a key pair locally. */
  let issueCSRPEM: string = $state("");
  let issueTTL: string = $state("8760h");
  let issueLoading: boolean = $state(false);
  let issuedCert: EnrolledCert | IssuedCertWithKey | null = $state(null);

  /** Enroll form state (bring-your-own certificate). */
  let enrollUsername: string = $state("");
  let enrollCertPEM: string = $state("");
  let enrollChainPEM: string = $state("");
  let enrollLoading: boolean = $state(false);
  let enrolledCert: EnrolledCert | null = $state(null);

  /** Export form state. */
  let exportCertPEM: string = $state("");
  let exportKeyPEM: string = $state("");
  let exportPassphrase: string = $state("");
  let exportLoading: boolean = $state(false);
  let exportedData: ExportCertResponse | null = $state(null);

  /** Revoke form state. */
  let revokeSerial: string = $state("");
  let revokeLoading: boolean = $state(false);
  let showRevokeConfirm: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Sorting and pagination
  // ---------------------------------------------------------------------------

  type SortKey = keyof CertRecord | "status";
  type SortDir = "asc" | "desc";

  let sortKey: SortKey = $state("issuedAt");
  let sortDir: SortDir = $state("desc");
  let currentPage: number = $state(1);
  const pageSize = 10;

  // ---------------------------------------------------------------------------
  // Derived
  // ---------------------------------------------------------------------------

  /**
   * Returns the computed status for a certificate record.
   * Certs expiring within 30 days are flagged "Expiring Soon".
   */
  function certStatus(cert: CertRecord): CertStatus {
    if (cert.revoked) return "Revoked";
    const thirtyDaysMs = 30 * 24 * 60 * 60 * 1000;
    const expiresAt = new Date(cert.expiresAt).getTime();
    if (expiresAt - Date.now() <= thirtyDaysMs) return "Expiring Soon";
    return "Active";
  }

  const sortedCerts = $derived.by(() => {
    const copy = [...certs];
    copy.sort((a, b) => {
      let av: unknown;
      let bv: unknown;
      if (sortKey === "status") {
        av = certStatus(a);
        bv = certStatus(b);
      } else {
        av = a[sortKey as keyof CertRecord];
        bv = b[sortKey as keyof CertRecord];
      }
      const as = String(av ?? "");
      const bs = String(bv ?? "");
      const cmp = as.localeCompare(bs);
      return sortDir === "asc" ? cmp : -cmp;
    });
    return copy;
  });

  const totalPages = $derived(Math.max(1, Math.ceil(sortedCerts.length / pageSize)));

  const pagedCerts = $derived(
    sortedCerts.slice((currentPage - 1) * pageSize, currentPage * pageSize),
  );

  /** True when the issued cert carries a locally-generated private key. */
  function hasGeneratedKey(cert: EnrolledCert | IssuedCertWithKey | null): cert is IssuedCertWithKey {
    return cert !== null && "keyPEM" in cert && typeof cert.keyPEM === "string" && cert.keyPEM !== "";
  }

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function toggleSort(key: SortKey): void {
    if (sortKey === key) {
      sortDir = sortDir === "asc" ? "desc" : "asc";
    } else {
      sortKey = key;
      sortDir = "asc";
    }
    currentPage = 1;
  }

  function sortIndicator(key: SortKey): string {
    if (sortKey !== key) return "";
    return sortDir === "asc" ? " ▲" : " ▼";
  }

  /** Serial of the row whose detail is expanded, or null. */
  let expandedSerial: string | null = $state(null);

  function toggleDetail(serial: string): void {
    expandedSerial = expandedSerial === serial ? null : serial;
  }

  /** Opens the Revoke tab with the given serial filled in (no request is sent). */
  function prefillRevoke(serial: string): void {
    revokeSerial = serial;
    showRevokeConfirm = false;
    activeTab = "revoke";
    actionPanelOpen = true;
  }

  /** Human-readable time until `expiresAt`, or "Expired". */
  function validityRemaining(expiresAt: string): string {
    const ms = new Date(expiresAt).getTime() - Date.now();
    if (Number.isNaN(ms)) return "";
    if (ms <= 0) return "Expired";
    const days = Math.floor(ms / 86_400_000);
    if (days >= 1) return `${days} day${days === 1 ? "" : "s"}`;
    const hours = Math.floor(ms / 3_600_000);
    return hours >= 1 ? `${hours} hour${hours === 1 ? "" : "s"}` : "less than an hour";
  }

  function formatDate(value: string): string {
    if (!value) return "";
    try {
      return new Date(value).toLocaleString();
    } catch {
      return value;
    }
  }

  function openTab(tab: ActionTab): void {
    if (actionPanelOpen && activeTab === tab) {
      actionPanelOpen = false;
    } else {
      activeTab = tab;
      actionPanelOpen = true;
    }
  }

  /** Triggers a browser download of the given text content. */
  function downloadText(filename: string, content: string): void {
    const blob = new Blob([content], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  /** Fetches the list of issued certificates for the selected tenant. */
  async function fetchCerts(): Promise<void> {
    try {
      certsLoading = true;
      const response: ListCertsResponse = await client.certs.list(filterTenantId);
      certs = [...(response.certs ?? [])];
      currentPage = 1;
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to fetch certificates";
      showToast("error", msg);
    } finally {
      certsLoading = false;
    }
  }

  /** Re-fetches certificates when the tenant filter changes. */
  function handleTenantFilterChange(): void {
    void fetchCerts();
  }

  /**
   * Issues a certificate for an existing account. When a CSR is pasted, the
   * caller's own key never leaves this form. When the CSR is left blank
   * (desktop only), a key pair is generated locally and returned alongside
   * the certificate for the user to keep — the server never sees it.
   */
  async function handleIssue(): Promise<void> {
    if (issueUsername.trim() === "") {
      showToast("warning", "Username is required");
      return;
    }
    if (issueCSRPEM.trim() === "" && !desktopAvailable) {
      showToast("warning", "A CSR is required (local key generation is desktop-only)");
      return;
    }

    try {
      issueLoading = true;
      if (issueCSRPEM.trim() === "") {
        issuedCert = await client.certs.issueCertWithLocalKey(issueUsername.trim(), issueTTL.trim());
      } else {
        issuedCert = await client.certs.issueCert({
          username: issueUsername.trim(),
          csrPEM: issueCSRPEM.trim(),
          ttl: issueTTL.trim(),
        });
      }
      showToast("success", `Certificate issued for "${issueUsername}"`);
      await fetchCerts();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to issue certificate";
      showToast("error", msg);
    } finally {
      issueLoading = false;
    }
  }

  /**
   * Enrolls a bring-your-own certificate on an existing account. The
   * certificate must chain to one of the tenant's registered trust anchors.
   */
  async function handleEnroll(): Promise<void> {
    if (enrollUsername.trim() === "") {
      showToast("warning", "Username is required");
      return;
    }
    if (enrollCertPEM.trim() === "") {
      showToast("warning", "Certificate PEM is required");
      return;
    }

    try {
      enrollLoading = true;
      enrolledCert = await client.certs.enrollCert({
        username: enrollUsername.trim(),
        certPEM: enrollCertPEM.trim(),
        ...(enrollChainPEM.trim() === "" ? {} : { chainPEM: enrollChainPEM.trim() }),
      });
      showToast("success", `Certificate enrolled for "${enrollUsername}"`);
      await fetchCerts();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to enroll certificate";
      showToast("error", msg);
    } finally {
      enrollLoading = false;
    }
  }

  /** Exports a certificate as PKCS#12. */
  async function handleExport(): Promise<void> {
    if (exportCertPEM.trim() === "") {
      showToast("warning", "Certificate PEM is required");
      return;
    }
    if (exportKeyPEM.trim() === "") {
      showToast("warning", "Private key PEM is required");
      return;
    }
    if (exportPassphrase.trim() === "") {
      showToast("warning", "Passphrase is required for PKCS#12 export");
      return;
    }

    try {
      exportLoading = true;
      const result: ExportCertResponse = await client.certs.export({
        certPEM: exportCertPEM.trim(),
        keyPEM: exportKeyPEM.trim(),
        passphrase: exportPassphrase.trim(),
      });
      exportedData = result;
      showToast("success", "Certificate exported successfully");
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to export certificate";
      showToast("error", msg);
    } finally {
      exportLoading = false;
    }
  }

  /** Revokes a certificate by serial number. */
  async function handleRevoke(): Promise<void> {
    if (revokeSerial.trim() === "") {
      showToast("warning", "Serial number is required");
      return;
    }

    try {
      revokeLoading = true;
      await client.certs.revoke(revokeSerial.trim());
      showToast("success", `Certificate ${revokeSerial} revoked`);
      revokeSerial = "";
      showRevokeConfirm = false;
      await fetchCerts();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to revoke certificate";
      showToast("error", msg);
    } finally {
      revokeLoading = false;
    }
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  onMount(() => {
    void fetchCerts();
  });
</script>

<PageHeader title="Certificates" description="Issue, enroll, export, and revoke mTLS client certificates" />

<div class="flex flex-col gap-6">
  <!-- Tenant filter -->
  <div class="flex items-center gap-3">
    <label class="form-control w-64" for="cert-filter-tenant">
      <div class="label"><span class="label-text font-semibold">Tenant</span></div>
      <TenantSelect
        id="cert-filter-tenant"
        {client}
        bind:value={filterTenantId}
        allowEmpty={true}
        size="sm"
        onchange={handleTenantFilterChange}
      />
    </label>
  </div>

  <!-- Tabbed action panel (collapsible) -->
  <div class="card bg-base-200 shadow">
    <!-- Tab bar / toggle header -->
    <div class="flex items-center gap-1 border-b border-base-content/10 px-4 py-2">
      <span class="mr-2 text-sm font-semibold text-base-content/70">Actions</span>
      <button
        class="btn btn-sm {activeTab === 'issue' && actionPanelOpen ? 'btn-primary' : 'btn-ghost'}"
        onclick={() => openTab("issue")}
      >
        Issue
      </button>
      <button
        class="btn btn-sm {activeTab === 'enroll' && actionPanelOpen ? 'btn-accent' : 'btn-ghost'}"
        onclick={() => openTab("enroll")}
      >
        Enroll
      </button>
      <button
        class="btn btn-sm {activeTab === 'export' && actionPanelOpen ? 'btn-secondary' : 'btn-ghost'}"
        onclick={() => openTab("export")}
      >
        Export PKCS#12
      </button>
      <button
        class="btn btn-sm {activeTab === 'revoke' && actionPanelOpen ? 'btn-warning' : 'btn-ghost'}"
        onclick={() => openTab("revoke")}
      >
        Revoke
      </button>
    </div>

    <!-- Panel body (shown only when open) -->
    {#if actionPanelOpen}
      <div class="card-body pt-4">
        <!-- Issue tab -->
        {#if activeTab === "issue"}
          <p class="text-base-content/70 text-sm">
            Issues a certificate for an EXISTING account of your own tenant, signed
            by the tenant's CA. Paste a CSR to keep your key off this machine, or
            leave it blank{#if desktopAvailable} to generate a key pair locally{/if}.
          </p>
          <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
            <label class="form-control w-full">
              <div class="label"><span class="label-text">Username</span></div>
              <input
                type="text"
                class="input input-bordered input-sm w-full"
                placeholder="e.g. alice"
                bind:value={issueUsername}
              />
            </label>
            <label class="form-control w-full md:col-span-2">
              <div class="label">
                <span class="label-text">CSR PEM</span>
                <span class="label-text-alt text-base-content/50">
                  {desktopAvailable ? "Optional — blank generates a key locally" : "Required"}
                </span>
              </div>
              <textarea
                class="textarea textarea-bordered w-full font-mono text-xs"
                rows="3"
                placeholder="-----BEGIN CERTIFICATE REQUEST-----"
                bind:value={issueCSRPEM}
              ></textarea>
            </label>
            <label class="form-control w-full">
              <div class="label"><span class="label-text">TTL</span></div>
              <input
                type="text"
                class="input input-bordered input-sm w-full"
                placeholder="8760h"
                bind:value={issueTTL}
              />
            </label>
          </div>
          <button
            class="btn btn-primary btn-sm mt-2 w-fit"
            onclick={() => void handleIssue()}
            disabled={issueLoading || issueUsername.trim() === "" || (issueCSRPEM.trim() === "" && !desktopAvailable)}
          >
            {#if issueLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Issue
          </button>

          {#if issuedCert !== null}
            <div class="mt-3 grid grid-cols-1 gap-3 lg:grid-cols-2">
              <div>
                <div class="label">
                  <span class="label-text text-xs font-semibold">Certificate PEM</span>
                </div>
                <textarea
                  class="textarea textarea-bordered w-full font-mono text-xs"
                  rows="8"
                  readonly
                  value={issuedCert.certPEM}
                ></textarea>
                <button
                  class="btn btn-ghost btn-xs mt-1"
                  onclick={() => downloadText(`${issueUsername || "cert"}.crt`, issuedCert?.certPEM ?? "")}
                >
                  Download Certificate
                </button>
              </div>
              {#if hasGeneratedKey(issuedCert)}
                <div>
                  <div class="label">
                    <span class="label-text text-xs font-semibold">Private Key PEM (generated locally)</span>
                  </div>
                  <textarea
                    class="textarea textarea-bordered w-full font-mono text-xs"
                    rows="8"
                    readonly
                    value={issuedCert.keyPEM}
                  ></textarea>
                  <button
                    class="btn btn-ghost btn-xs mt-1"
                    onclick={() => downloadText(`${issueUsername || "cert"}.key`, hasGeneratedKey(issuedCert) ? issuedCert.keyPEM : "")}
                  >
                    Download Key
                  </button>
                </div>
              {/if}
            </div>
          {/if}
        {/if}

        <!-- Enroll tab -->
        {#if activeTab === "enroll"}
          <p class="text-base-content/70 text-sm">
            Enrolls a bring-your-own certificate on an EXISTING account of your own
            tenant. The certificate must chain to one of the tenant's registered
            trust anchors.
          </p>
          <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
            <label class="form-control w-full">
              <div class="label"><span class="label-text">Username</span></div>
              <input
                type="text"
                class="input input-bordered input-sm w-full"
                placeholder="e.g. alice"
                bind:value={enrollUsername}
              />
            </label>
          </div>
          <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <label class="form-control w-full">
              <div class="label"><span class="label-text text-xs font-semibold">Certificate PEM</span></div>
              <textarea
                class="textarea textarea-bordered w-full font-mono text-xs"
                rows="5"
                placeholder="Paste certificate PEM here (leaf first)"
                bind:value={enrollCertPEM}
              ></textarea>
            </label>
            <label class="form-control w-full">
              <div class="label">
                <span class="label-text text-xs font-semibold">Chain PEM</span>
                <span class="label-text-alt text-base-content/50">Optional</span>
              </div>
              <textarea
                class="textarea textarea-bordered w-full font-mono text-xs"
                rows="5"
                placeholder="Paste intermediate chain PEM here"
                bind:value={enrollChainPEM}
              ></textarea>
            </label>
          </div>
          <button
            class="btn btn-accent btn-sm mt-2 w-fit"
            onclick={() => void handleEnroll()}
            disabled={enrollLoading || enrollUsername.trim() === "" || enrollCertPEM.trim() === ""}
          >
            {#if enrollLoading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Enroll
          </button>

          {#if enrolledCert !== null}
            <div class="mt-3">
              <div class="label">
                <span class="label-text text-xs font-semibold">Enrolled Certificate</span>
              </div>
              <textarea
                class="textarea textarea-bordered w-full font-mono text-xs"
                rows="6"
                readonly
                value={enrolledCert.certPEM}
              ></textarea>
            </div>
          {/if}
        {/if}

        <!-- Export tab -->
        {#if activeTab === "export"}
          <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <div class="flex flex-col gap-3">
              <label class="form-control w-full">
                <div class="label"><span class="label-text text-xs font-semibold">Certificate PEM</span></div>
                <textarea
                  class="textarea textarea-bordered w-full font-mono text-xs"
                  rows="5"
                  placeholder="Paste certificate PEM here"
                  bind:value={exportCertPEM}
                ></textarea>
              </label>
              <label class="form-control w-full">
                <div class="label"><span class="label-text text-xs font-semibold">Private Key PEM</span></div>
                <textarea
                  class="textarea textarea-bordered w-full font-mono text-xs"
                  rows="5"
                  placeholder="Paste private key PEM here"
                  bind:value={exportKeyPEM}
                ></textarea>
              </label>
              <label class="form-control w-full">
                <div class="label"><span class="label-text text-xs font-semibold">Passphrase</span></div>
                <input
                  type="password"
                  class="input input-bordered input-sm"
                  placeholder="PKCS#12 passphrase"
                  bind:value={exportPassphrase}
                />
              </label>
              <button
                class="btn btn-secondary btn-sm w-fit"
                onclick={() => void handleExport()}
                disabled={
                  exportLoading ||
                  exportCertPEM.trim() === "" ||
                  exportKeyPEM.trim() === "" ||
                  exportPassphrase.trim() === ""
                }
              >
                {#if exportLoading}
                  <span class="loading loading-spinner loading-xs"></span>
                {/if}
                Export PKCS#12
              </button>
            </div>

            {#if exportedData !== null}
              <div class="flex flex-col gap-3">
                <div class="label">
                  <span class="label-text text-xs font-semibold">PKCS#12 Data (Base64)</span>
                </div>
                <textarea
                  class="textarea textarea-bordered w-full font-mono text-xs"
                  rows="10"
                  readonly
                  value={exportedData.pkcs12}
                ></textarea>
              </div>
            {/if}
          </div>
        {/if}

        <!-- Revoke tab -->
        {#if activeTab === "revoke"}
          <p class="text-base-content/70 text-sm">
            Permanently revokes a certificate by serial number. This action cannot be undone.
          </p>
          <div class="mt-3 flex flex-col gap-3">
            <label class="form-control w-full max-w-sm">
              <div class="label"><span class="label-text text-xs font-semibold">Certificate Serial Number</span></div>
              <input
                type="text"
                class="input input-bordered input-sm"
                placeholder="e.g. 1a2b3c4d..."
                bind:value={revokeSerial}
              />
            </label>

            {#if showRevokeConfirm}
              <div class="alert alert-warning">
                <span>Are you sure? Revoking serial "{revokeSerial}" is permanent.</span>
              </div>
              <div class="flex gap-2">
                <button
                  class="btn btn-error btn-sm"
                  onclick={() => void handleRevoke()}
                  disabled={revokeLoading}
                >
                  {#if revokeLoading}
                    <span class="loading loading-spinner loading-xs"></span>
                  {/if}
                  Confirm Revoke
                </button>
                <button
                  class="btn btn-ghost btn-sm"
                  onclick={() => { showRevokeConfirm = false; }}
                >
                  Cancel
                </button>
              </div>
            {:else}
              <button
                class="btn btn-warning btn-sm w-fit"
                title="Permanently revoke this certificate. The certificate will be rejected by the server and cannot be re-activated."
                onclick={() => { showRevokeConfirm = true; }}
                disabled={revokeSerial.trim() === ""}
              >
                Revoke
              </button>
            {/if}
          </div>
        {/if}
      </div>
    {/if}
  </div>

  <!-- Issued certificates table (primary view) -->
  <div class="flex flex-col">
    <!-- Table toolbar -->
    <div class="flex items-center justify-between rounded-t-lg bg-base-200 px-4 py-3">
      <div class="flex items-center gap-2">
        <h3 class="text-sm font-semibold">Issued Certificates</h3>
        <span class="badge badge-ghost badge-sm">{certs.length}</span>
      </div>
      <button
        class="btn btn-ghost btn-sm"
        onclick={() => void fetchCerts()}
        disabled={certsLoading}
      >
        {#if certsLoading}
          <span class="loading loading-spinner loading-xs"></span>
        {:else}
          <RefreshCw class="h-4 w-4" />
        {/if}
        Refresh
      </button>
    </div>

    <!-- Table body -->
    {#if certsLoading && certs.length === 0}
      <div class="flex justify-center bg-base-100 py-10">
        <span class="loading loading-spinner loading-md text-primary"></span>
      </div>
    {:else if certs.length === 0}
      <div class="bg-base-100 px-4 py-10 text-center">
        <p class="text-base-content/50 text-sm">No certificates issued.</p>
      </div>
    {:else}
      <div class="overflow-x-auto rounded-b-lg bg-base-100">
        <table class="table table-zebra table-sm w-full">
          <thead>
            <tr>
              <th>
                <button
                  class="flex items-center gap-1 font-semibold hover:text-primary"
                  onclick={() => toggleSort("serial")}
                >
                  Serial{sortIndicator("serial")}
                </button>
              </th>
              <th>
                <button
                  class="flex items-center gap-1 font-semibold hover:text-primary"
                  onclick={() => toggleSort("username")}
                >
                  Username{sortIndicator("username")}
                </button>
              </th>
              <th>
                <button
                  class="flex items-center gap-1 font-semibold hover:text-primary"
                  onclick={() => toggleSort("tenantID")}
                >
                  Tenant{sortIndicator("tenantID")}
                </button>
              </th>
              <th>
                <button
                  class="flex items-center gap-1 font-semibold hover:text-primary"
                  onclick={() => toggleSort("issuedAt")}
                >
                  Issued At{sortIndicator("issuedAt")}
                </button>
              </th>
              <th>
                <button
                  class="flex items-center gap-1 font-semibold hover:text-primary"
                  onclick={() => toggleSort("expiresAt")}
                >
                  Expires At{sortIndicator("expiresAt")}
                </button>
              </th>
              <th>
                <button
                  class="flex items-center gap-1 font-semibold hover:text-primary"
                  onclick={() => toggleSort("status")}
                >
                  Status{sortIndicator("status")}
                </button>
              </th>
              <th><span class="sr-only">Detail</span></th>
            </tr>
          </thead>
          <tbody>
            {#each pagedCerts as cert (cert.serial)}
              {@const status = certStatus(cert)}
              <tr>
                <td class="font-mono text-xs">{cert.serial}</td>
                <td>{cert.username}</td>
                <td>{cert.tenantID}</td>
                <td class="whitespace-nowrap text-xs">{formatDate(cert.issuedAt)}</td>
                <td class="whitespace-nowrap text-xs">{formatDate(cert.expiresAt)}</td>
                <td>
                  {#if status === "Revoked"}
                    <span class="badge badge-error badge-sm">Revoked</span>
                  {:else if status === "Expiring Soon"}
                    <span class="badge badge-warning badge-sm">Expiring Soon</span>
                  {:else}
                    <span class="badge badge-success badge-sm">Active</span>
                  {/if}
                </td>
                <td class="text-right">
                  <button
                    class="btn btn-ghost btn-xs"
                    aria-expanded={expandedSerial === cert.serial}
                    aria-controls="cert-detail-{cert.serial}"
                    onclick={() => toggleDetail(cert.serial)}
                  >
                    {expandedSerial === cert.serial ? "Hide" : "Details"}
                  </button>
                </td>
              </tr>
              {#if expandedSerial === cert.serial}
                <tr id="cert-detail-{cert.serial}" data-testid="cert-detail">
                  <td colspan="7" class="bg-base-200">
                    <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 text-xs">
                      <dt class="font-semibold">Serial</dt>
                      <dd class="font-mono break-all">{cert.serial}</dd>
                      <dt class="font-semibold">Username</dt>
                      <dd>{cert.username}</dd>
                      <dt class="font-semibold">Tenant</dt>
                      <dd>{cert.tenantID}</dd>
                      <dt class="font-semibold">Issued at (UTC)</dt>
                      <dd class="font-mono">{cert.issuedAt}</dd>
                      <dt class="font-semibold">Expires at (UTC)</dt>
                      <dd class="font-mono">{cert.expiresAt}</dd>
                      <dt class="font-semibold">Validity remaining</dt>
                      <dd>{cert.revoked ? "—" : validityRemaining(cert.expiresAt)}</dd>
                      <dt class="font-semibold">Status</dt>
                      <dd>{status}</dd>
                    </dl>
                    {#if !cert.revoked}
                      <button
                        class="btn btn-warning btn-xs mt-2"
                        onclick={() => prefillRevoke(cert.serial)}
                      >
                        Revoke this certificate…
                      </button>
                    {/if}
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>

      <!-- Pagination footer -->
      {#if totalPages > 1}
        <div class="flex items-center justify-between rounded-b-lg border-t border-base-content/10 bg-base-200 px-4 py-2">
          <span class="text-base-content/60 text-xs">
            Page {currentPage} of {totalPages} ({certs.length} total)
          </span>
          <div class="join">
            <button
              class="join-item btn btn-xs"
              disabled={currentPage === 1}
              onclick={() => { currentPage = 1; }}
            >
              «
            </button>
            <button
              class="join-item btn btn-xs"
              disabled={currentPage === 1}
              onclick={() => { currentPage -= 1; }}
            >
              ‹
            </button>
            <button class="join-item btn btn-xs btn-active pointer-events-none">
              {currentPage}
            </button>
            <button
              class="join-item btn btn-xs"
              disabled={currentPage === totalPages}
              onclick={() => { currentPage += 1; }}
            >
              ›
            </button>
            <button
              class="join-item btn btn-xs"
              disabled={currentPage === totalPages}
              onclick={() => { currentPage = totalPages; }}
            >
              »
            </button>
          </div>
        </div>
      {/if}
    {/if}
  </div>
</div>
