<script lang="ts">
  import { loadDiagnosticHistory, loadDiagnostics } from "../lib/api";
  import type { BrowserProbe, DiagnosticCheck, DiagnosticReport, DiagnosticStatus, HealthSnapshot, Route } from "../lib/types";

  let { route, baseDomain, onClose, onNotice }: { route: Route; baseDomain: string; onClose: () => void; onNotice: (message: string) => void } = $props();
  let report = $state<DiagnosticReport | null>(null);
  let loading = $state(false);
  let error = $state("");
  let history = $state<HealthSnapshot[]>([]);
  let historyIntervalMs = $state(300000);
  let historyError = $state("");
  let browserProbe = $state<BrowserProbe>({ status: "idle", summary: "Browser probe has not run" });
  let loadedRouteId = $state<number | null>(null);
  let actionable = $derived(report?.checks.filter((check) => check.status !== "pass") || []);
  let chronological = $derived([...history].reverse());

  $effect(() => {
    if (loadedRouteId === route.id) return;
    loadedRouteId = route.id;
    void diagnose();
  });

  function groupedChecks(checks: DiagnosticCheck[]) {
    const groups = new Map<string, DiagnosticCheck[]>();
    for (const check of checks) groups.set(check.layer, [...(groups.get(check.layer) || []), check]);
    return Array.from(groups, ([layer, entries]) => ({ layer, entries }));
  }
  function statusLabel(status: BrowserProbe["status"] | DiagnosticStatus) {
    if (status === "pending") return "Checking";
    if (status === "idle") return "Not checked";
    if (status === "pass") return "Working";
    if (status === "warn") return "Attention";
    return "Problem";
  }
  function controllerSummary() {
    if (loading && !report) return "Checking the route…";
    if (!report) return "Controller result unavailable";
    if (report.status === "pass") return "Docklane can reach the container";
    if (report.status === "warn") return "The route works, with warnings";
    return "Docklane found a routing problem";
  }
  function historyCount(status: DiagnosticStatus) { return history.filter((snapshot) => snapshot.status === status).length; }
  function intervalLabel(milliseconds: number) { const minutes = Math.round(milliseconds / 60000); return minutes === 1 ? "1 minute" : `${minutes} minutes`; }

  async function diagnose() {
    report = null; error = ""; history = []; historyError = ""; loading = true;
    browserProbe = { status: "pending", summary: "Testing browser access…" };
    await Promise.allSettled([loadController(), probeBrowser()]);
    loading = false;
  }
  async function loadController() {
    try {
      report = await loadDiagnostics(route.id);
      try {
        const payload = await loadDiagnosticHistory(route.id);
        history = payload.snapshots;
        historyIntervalMs = payload.sampleIntervalMs;
      } catch (cause) { historyError = cause instanceof Error ? cause.message : "History loading failed"; }
    } catch (cause) { error = cause instanceof Error ? cause.message : "Diagnostics failed"; }
  }
  async function probeBrowser() {
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 7000);
    try {
      await fetch(`https://${route.name}.${baseDomain}/`, { method: "GET", mode: "no-cors", cache: "no-store", signal: controller.signal });
      browserProbe = { status: "pass", summary: "Route opens in this browser" };
    } catch (cause) {
      browserProbe = { status: "fail", summary: "This browser cannot open the route", detail: cause instanceof Error ? cause.message : "Check local DNS and certificate trust in this browser." };
    } finally { window.clearTimeout(timeout); }
  }
  async function copyReport() {
    try {
      await navigator.clipboard.writeText(JSON.stringify({ controller: report, browser: browserProbe }, null, 2));
      onNotice(`Copied diagnostics for ${route.name}`);
    } catch { error = "Clipboard access was unavailable"; }
  }
</script>

<section class="diagnostics card bg-base-100" aria-labelledby="diagnostics-title">
  <button class="btn btn-ghost btn-sm back-link" type="button" onclick={onClose}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m15 18-6-6 6-6"></path></svg>Back to routes</button>
  <div class="diagnostics-header"><div><p class="eyebrow">DIAGNOSTICS</p><h2 id="diagnostics-title">{route.name}.{baseDomain}</h2><p>See whether the route works here and where it needs attention.</p></div><div class="diagnostics-actions"><button class="btn btn-outline btn-sm secondary small" onclick={diagnose} disabled={loading}>{loading ? "Checking…" : "Refresh checks"}</button><button class="btn btn-ghost btn-sm ghost small" onclick={copyReport}>Copy report</button></div></div>
  {#if error}<div class="alert alert-error diagnostic-error" role="alert"><span>{error}</span></div>{/if}
  <div class="diagnostic-overview">
    <article class="diagnostic-result"><div class="diagnostic-result-heading"><span class="perspective-label">This browser</span><span class={`status-pill ${browserProbe.status}`}>{statusLabel(browserProbe.status)}</span></div><strong>{browserProbe.summary}</strong>{#if browserProbe.status === "fail" && browserProbe.detail}<small>{browserProbe.detail}</small>{/if}</article>
    <article class="diagnostic-result"><div class="diagnostic-result-heading"><span class="perspective-label">Docklane</span><span class={`status-pill ${report?.status || (loading ? "pending" : "fail")}`}>{report ? statusLabel(report.status) : loading ? "Checking" : "Unavailable"}</span></div><strong>{controllerSummary()}</strong>{#if report}<small>Checked at {new Date(report.generatedAt).toLocaleTimeString()}</small>{/if}</article>
  </div>
  {#if report}
    {#if actionable.length > 0}<section class="diagnostic-attention" aria-labelledby="attention-title"><div><p class="eyebrow">NEEDS ATTENTION</p><h3 id="attention-title">{actionable.length} item{actionable.length === 1 ? "" : "s"} to check</h3></div><div class="attention-list">{#each actionable as check}<div class="attention-item"><span class={`check-mark ${check.status}`}>{check.status === "warn" ? "!" : "×"}</span><div><strong>{check.summary}</strong>{#if check.suggestion}<p>{check.suggestion}</p>{:else if check.detail}<p>{check.detail}</p>{/if}</div></div>{/each}</div></section>{/if}
    <details class="technical-details"><summary><span>Technical details</span><small>{report.checks.length} checks</small></summary>
      <div class="health-history"><div class="history-heading"><div><span class="perspective-label">Recent checks</span><strong>{history.length} saved result{history.length === 1 ? "" : "s"}</strong></div><small>Runs every {intervalLabel(historyIntervalMs)}</small></div>
        {#if historyError}<p class="history-error">{historyError}</p>{:else if history.length > 0}<div class="history-timeline" aria-label="Recent controller health">{#each chronological as snapshot}<span class={`history-point ${snapshot.status}`} title={`${new Date(snapshot.recordedAt).toLocaleString()} · ${snapshot.status}`} aria-label={`${snapshot.status} at ${new Date(snapshot.recordedAt).toLocaleString()}`}></span>{/each}</div><div class="history-legend"><span><i class="pass"></i>{historyCount("pass")} working</span><span><i class="warn"></i>{historyCount("warn")} warning</span><span><i class="fail"></i>{historyCount("fail")} problem</span></div>{:else}<p class="history-empty">No saved results yet.</p>{/if}
      </div>
      <div class="diagnostic-groups">{#each groupedChecks(report.checks) as group}<div class="diagnostic-group"><h3>{group.layer}</h3>{#each group.entries as check}<div class="diagnostic-check"><span class={`check-mark ${check.status}`}>{check.status === "pass" ? "✓" : check.status === "warn" ? "!" : "×"}</span><div><strong>{check.summary}</strong>{#if check.detail}<p>{check.detail}</p>{/if}{#if check.suggestion}<p class="repair">Try: {check.suggestion}</p>{/if}</div></div>{/each}</div>{/each}</div>
    </details>
  {/if}
</section>
