<script lang="ts">
  import { onDestroy } from "svelte";
  import ConfirmDialog from "./ConfirmDialog.svelte";
  import RouteDiagnostics from "./RouteDiagnostics.svelte";
  import RouteList from "./RouteList.svelte";
  import RouteSummary from "./RouteSummary.svelte";
  import { deleteRoute, loadRouteReadiness, updateRoute } from "../lib/api";
  import { defaultReadiness, filterRoutes, writableRoute } from "../lib/route-utils";
  import type { Route, RouteReadiness } from "../lib/types";

  let { routes, baseDomain, reconcileEverySeconds, loading, loadError, highlightedRouteId, onRefresh, onNewRoute, onEdit, onNotice, onError }: {
    routes: Route[];
    baseDomain: string;
    reconcileEverySeconds: number;
    loading: boolean;
    loadError: string;
    highlightedRouteId: number | null;
    onRefresh: (showLoading?: boolean) => void | Promise<void>;
    onNewRoute: () => void;
    onEdit: (route: Route) => void;
    onNotice: (message: string) => void;
    onError: (message: string) => void;
  } = $props();

  let search = $state("");
  let readiness = $state<Record<number, RouteReadiness>>({});
  let openMenuId = $state<number | null>(null);
  let pendingRouteIds = $state(new Set<number>());
  let deleteCandidate = $state<Route | null>(null);
  let diagnosticRoute = $state<Route | null>(null);
  const polling = new Map<number, number>();
  let mounted = true;

  let filtered = $derived(filterRoutes(routes, baseDomain, search));
  let readyCount = $derived(routes.filter((route) => readinessFor(route).ready).length);
  let publishingCount = $derived(routes.filter((route) => ["reconciling", "publishing", "verifying"].includes(readinessFor(route).state)).length);
  let attentionCount = $derived(routes.filter((route) => readinessFor(route).state === "error" || ["unresolved", "ambiguous", "error"].includes(route.observed?.state ?? "")).length);
  let routeVersions = $derived(routes.map((route) => `${route.id}:${route.revision}`).join(","));

  $effect(() => {
    routeVersions;
    for (const route of routes) void ensureReadiness(route);
  });
  onDestroy(() => { mounted = false; });

  function readinessFor(route: Route) {
    const current = readiness[route.id];
    return current?.revision === route.revision ? current : defaultReadiness(route);
  }
  function wait(milliseconds: number) { return new Promise((resolve) => window.setTimeout(resolve, milliseconds)); }
  async function ensureReadiness(route: Route) {
    if (polling.get(route.id) === route.revision) return;
    polling.set(route.id, route.revision);
    const deadline = Date.now() + 30_000;
    try {
      while (mounted) {
        const current = routes.find((candidate) => candidate.id === route.id);
        if (!current || current.revision !== route.revision) return;
        try {
          const payload = await loadRouteReadiness(route.id);
          readiness = { ...readiness, [route.id]: payload };
          if (payload.ready || payload.state === "disabled" || payload.state === "error") return;
        } catch (cause) {
          readiness = { ...readiness, [route.id]: { routeId: route.id, revision: route.revision, state: "publishing", ready: false, message: cause instanceof Error ? cause.message : "Readiness check is temporarily unavailable.", checkedAt: new Date().toISOString() } };
        }
        if (Date.now() >= deadline) {
          readiness = { ...readiness, [route.id]: { ...defaultReadiness(route), state: "error", message: "Route activation is taking longer than 30 seconds. Open Diagnose for the failing layer." } };
          return;
        }
        await wait(600);
      }
    } finally { if (polling.get(route.id) === route.revision) polling.delete(route.id); }
  }
  function setPending(id: number, pending: boolean) {
    const next = new Set(pendingRouteIds);
    if (pending) next.add(id); else next.delete(id);
    pendingRouteIds = next;
  }
  async function toggle(route: Route) {
    openMenuId = null; setPending(route.id, true);
    try {
      await updateRoute(route.id, writableRoute(route, !route.enabled));
      onNotice(`${route.enabled ? "Disabled" : "Enabled"} ${route.name}`);
      await onRefresh(false);
    } catch (cause) { onError(cause instanceof Error ? cause.message : "Route update failed"); }
    finally { setPending(route.id, false); }
  }
  async function remove() {
    if (!deleteCandidate) return;
    const route = deleteCandidate;
    setPending(route.id, true);
    try {
      await deleteRoute(route.id);
      if (diagnosticRoute?.id === route.id) diagnosticRoute = null;
      onNotice(`Deleted ${route.name}.${baseDomain}`);
      deleteCandidate = null;
      await onRefresh(false);
    } catch (cause) { onError(cause instanceof Error ? cause.message : "Route deletion failed"); }
    finally { setPending(route.id, false); }
  }
</script>

{#if diagnosticRoute}
  <RouteDiagnostics route={diagnosticRoute} {baseDomain} onClose={() => (diagnosticRoute = null)} {onNotice} />
{:else}
  <section aria-labelledby="routes-title">
    <header class="page-header"><div><p class="eyebrow">LOCAL ROUTING</p><h1 id="routes-title">Routes</h1><p>Stable HTTPS names for local container workloads.</p></div><button class="btn btn-primary primary-action" type="button" onclick={onNewRoute}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"></path></svg>New route</button></header>
    <RouteSummary total={routes.length} ready={readyCount} publishing={publishingCount} attention={attentionCount} />
    <div class="list-toolbar">
      <div class="title-with-count"><h2>All routes</h2><span>{search ? `${filtered.length} of ${routes.length}` : `syncs every ${reconcileEverySeconds}s`}</span></div>
      <div class="panel-toolbar"><label class="input search-field"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="6"></circle><path d="m16 16 4 4"></path></svg><input type="search" bind:value={search} placeholder="Search routes" aria-label="Search routes" />{#if search}<button class="btn btn-circle btn-ghost btn-xs clear-search" type="button" aria-label="Clear route search" onclick={() => (search = "")}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m7 7 10 10M17 7 7 17"></path></svg></button>{/if}</label><button class="btn btn-square btn-outline icon-button secondary" type="button" aria-label="Refresh routes" title="Refresh routes" onclick={() => onRefresh()} disabled={loading} aria-busy={loading}>{#if loading}<span class="loading loading-spinner loading-sm"></span>{:else}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6v5h-5M4 18v-5h5"></path><path d="M6.1 9A7 7 0 0 1 18.7 7.3L20 11M4 13l1.3 3.7A7 7 0 0 0 17.9 15"></path></svg>{/if}</button></div>
    </div>
    {#if loading && routes.length === 0}<div class="state-panel card bg-base-100" role="status"><span class="loading loading-spinner loading-md" aria-hidden="true"></span><div><h3>Loading routes</h3><p>Checking the local controller…</p></div></div>
    {:else if loadError && routes.length === 0}<div class="state-panel state-error card bg-base-100" role="alert"><span class="state-icon">!</span><div><h3>Routes are unavailable</h3><p>{loadError}</p></div><button class="btn btn-outline btn-sm" type="button" onclick={() => onRefresh()}>Try again</button></div>
    {:else if routes.length === 0}<div class="empty card bg-base-200"><span class="empty-icon"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 7h14M5 12h14M5 17h8"></path></svg></span><h3>No routes yet</h3><p>Choose a running container and give it a stable local hostname.</p><button class="btn btn-primary" type="button" onclick={onNewRoute}>Browse containers</button></div>
    {:else if filtered.length === 0}<div class="empty compact">No routes match “{search}”.</div>
    {:else}<RouteList routes={filtered} {baseDomain} {readinessFor} {highlightedRouteId} {openMenuId} {pendingRouteIds} onMenu={(route) => (openMenuId = openMenuId === route.id ? null : route.id)} onDiagnose={(route) => { openMenuId = null; diagnosticRoute = route; }} onEdit={(route) => { openMenuId = null; onEdit(route); }} onToggle={toggle} onDelete={(route) => { openMenuId = null; deleteCandidate = route; }} />{/if}
  </section>
{/if}

<ConfirmDialog open={!!deleteCandidate} title="Delete route?" description={deleteCandidate ? `${deleteCandidate.name}.${baseDomain} will stop resolving through Docklane. The container is not changed.` : ""} confirmLabel="Delete route" danger alert busy={!!deleteCandidate && pendingRouteIds.has(deleteCandidate.id)} onCancel={() => (deleteCandidate = null)} onConfirm={remove} />
