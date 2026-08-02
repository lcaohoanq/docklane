<script lang="ts">
  import RouteRow from "./RouteRow.svelte";
  import type { Route, RouteReadiness } from "../lib/types";

  let { routes, baseDomain, readinessFor, highlightedRouteId, openMenuId, pendingRouteIds, onMenu, onDiagnose, onEdit, onToggle, onDelete }: {
    routes: Route[];
    baseDomain: string;
    readinessFor: (route: Route) => RouteReadiness;
    highlightedRouteId: number | null;
    openMenuId: number | null;
    pendingRouteIds: Set<number>;
    onMenu: (route: Route) => void;
    onDiagnose: (route: Route) => void;
    onEdit: (route: Route) => void;
    onToggle: (route: Route) => void;
    onDelete: (route: Route) => void;
  } = $props();
</script>

<div class="route-list card bg-base-100">
  <div class="route-list-head" aria-hidden="true"><span>Status</span><span>Hostname</span><span>Workload</span><span>Upstream</span><span></span></div>
  {#each routes as route}
    <RouteRow {route} readiness={readinessFor(route)} {baseDomain} highlighted={highlightedRouteId === route.id} menuOpen={openMenuId === route.id} pending={pendingRouteIds.has(route.id)} onMenu={() => onMenu(route)} onDiagnose={() => onDiagnose(route)} onEdit={() => onEdit(route)} onToggle={() => onToggle(route)} onDelete={() => onDelete(route)} />
  {/each}
</div>
