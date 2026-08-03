<script lang="ts">
  import { onMount } from "svelte";
  import { Toaster, toast } from "svelte-sonner";
  import AppNav from "./components/AppNav.svelte";
  import ContainersPage from "./components/ContainersPage.svelte";
  import RouteEditor from "./components/RouteEditor.svelte";
  import RoutesPage from "./components/RoutesPage.svelte";
  import { loadInventory } from "./lib/api";
  import { navigateToTab, pathForTab, tabFromPath } from "./lib/navigation";
  import { matchesSelector } from "./lib/route-utils";
  import type { ActiveTab, Container, Route, Theme } from "./lib/types";

  let containers = $state<Container[]>([]);
  let routes = $state<Route[]>([]);
  let baseDomain = $state("docker.home.arpa");
  let reconcileEverySeconds = $state(5);
  let loading = $state(true);
  let loadError = $state("");
  let inventoryLoaded = $state(false);
  let activeTab = $state<ActiveTab>("routes");
  let theme = $state<Theme>("forest");
  let selected = $state<Container | null>(null);
  let editing = $state<Route | null>(null);
  let highlightedRouteId = $state<number | null>(null);
  const refreshErrorToastId = "inventory-refresh-error";

  async function refresh(showLoading = true) {
    if (showLoading) {
      loading = true;
      loadError = "";
    }
    try {
      const inventory = await loadInventory();
      containers = inventory.containers;
      routes = inventory.routes;
      baseDomain = inventory.baseDomain;
      reconcileEverySeconds = Math.max(
        1,
        Math.round((inventory.reconcileIntervalMs || 5000) / 1000),
      );
      if (selected && !editing && !containers.some((container) => container.id === selected?.id))
        closeEditor();
      loadError = "";
      inventoryLoaded = true;
      toast.dismiss(refreshErrorToastId);
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : "Refresh failed";
      loadError = message;
      if (inventoryLoaded) toast.error(message, { id: refreshErrorToastId });
    } finally {
      if (showLoading) loading = false;
    }
  }

  function closeEditor() {
    selected = null;
    editing = null;
  }

  function showTab(tab: ActiveTab, updateHistory = true) {
    closeEditor();
    activeTab = tab;
    if (updateHistory) navigateToTab(tab);
  }

  function choose(container: Container) {
    activeTab = "containers";
    selected = container;
    editing = null;
  }

  function edit(route: Route) {
    activeTab = "routes";
    editing = route;
    selected = containers.find((container) => matchesSelector(route.selector, container)) || null;
  }

  async function editorSaved(message: string, routeId: number | null) {
    toast.success(message);
    highlightedRouteId = routeId;
    closeEditor();
    showTab("routes");
    await refresh(false);
    window.setTimeout(() => (highlightedRouteId = null), 3200);
  }

  function applyTheme(next: Theme) {
    theme = next;
    document.documentElement.dataset.theme = next;
    localStorage.setItem("docklane-theme", next);
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute("content", next === "forest" ? "#171212" : "#ffffff");
  }

  onMount(() => {
    theme = document.documentElement.dataset.theme === "light" ? "light" : "forest";
    applyTheme(theme);
    activeTab = tabFromPath(window.location.pathname);
    if (window.location.pathname !== pathForTab(activeTab)) navigateToTab(activeTab, true);

    const handlePopState = () => {
      const requestedTab = tabFromPath(window.location.pathname);
      if ((selected || editing) && requestedTab !== activeTab) {
        navigateToTab(activeTab);
        return;
      }
      showTab(requestedTab, false);
    };
    window.addEventListener("popstate", handlePopState);
    void refresh();
    const timer = window.setInterval(() => void refresh(false), 5000);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("popstate", handlePopState);
    };
  });
</script>

<svelte:head><title>Docklane · Local container gateway</title></svelte:head>
<svelte:body class:modal-open={!!selected || !!editing} />
<Toaster position="top-right" closeButton theme={theme === "forest" ? "dark" : "light"} />

<main>
  <AppNav
    {activeTab}
    routeCount={routes.length}
    containerCount={containers.length}
    {theme}
    onNavigate={showTab}
    onThemeChange={applyTheme}
  />

  {#if activeTab === "routes"}
    <RoutesPage
      {routes}
      {baseDomain}
      {reconcileEverySeconds}
      {loading}
      {loadError}
      {highlightedRouteId}
      onRefresh={refresh}
      onNewRoute={() => showTab("containers")}
      onEdit={edit}
      onNotice={(message) => toast.success(message)}
      onError={(message) => toast.error(message)}
    />
  {:else}
    <ContainersPage
      {containers}
      {loading}
      {loadError}
      onRefresh={() => refresh()}
      onChoose={choose}
    />
  {/if}
</main>

{#if selected || editing}
  <RouteEditor
    {selected}
    {editing}
    {routes}
    {baseDomain}
    onClose={closeEditor}
    onSaved={editorSaved}
    onError={(message) => toast.error(message)}
  />
{/if}
