<script lang="ts">
  import { onMount, tick } from "svelte";
  import logoUrl from "../../brand/logo-mark.svg";

  type ActiveTab = "routes" | "containers";

  type Container = {
    id: string;
    name: string;
    image: string;
    status: string;
    systemRole?: "reverse-proxy";
    composeProject?: string;
    composeService?: string;
    exposedPorts: number[];
  };

  type Selector = {
    composeProject: string;
    composeService: string;
    containerId?: string;
  };

  type Observation = {
    state: "ready" | "disabled" | "unresolved" | "ambiguous" | "error";
    message?: string;
    containerName?: string;
    upstreamUrl?: string;
    checkedAt?: string;
  };

  type Route = {
    id: number;
    revision: number;
    name: string;
    selector: Selector;
    port: number;
    scheme: string;
    enabled: boolean;
    observed: Observation;
  };

  type RouteReadiness = {
    routeId: number;
    revision: number;
    state:
      | "reconciling"
      | "publishing"
      | "verifying"
      | "ready"
      | "disabled"
      | "error";
    ready: boolean;
    message: string;
    checkedAt: string;
  };

  type DiagnosticStatus = "pass" | "warn" | "fail";

  type DiagnosticCheck = {
    id: string;
    layer: string;
    status: DiagnosticStatus;
    summary: string;
    detail?: string;
    suggestion?: string;
  };

  type DiagnosticReport = {
    status: DiagnosticStatus;
    target: string;
    hostname: string;
    generatedAt: string;
    checks: DiagnosticCheck[];
  };

  type BrowserProbe = {
    status: "idle" | "pending" | DiagnosticStatus;
    summary: string;
    detail?: string;
  };

  type HealthSnapshot = {
    id: number;
    routeId: number;
    status: DiagnosticStatus;
    recordedAt: string;
    report: DiagnosticReport;
  };

  let containers: Container[] = [];
  let routes: Route[] = [];
  let baseDomain = "docker.home.arpa";
  let reconcileEverySeconds = 5;
  let loading = true;
  let error = "";
  let notice = "";
  let activeTab: ActiveTab = "routes";
  let routeSearch = "";
  let containerSearch = "";
  let selected: Container | null = null;
  let editing: Route | null = null;
  let routeName = "";
  let routePort = 80;
  let routeScheme = "http";
  let saving = false;
  let routeReadiness: Record<number, RouteReadiness> = {};
  const readinessPolling = new Map<number, number>();
  let mounted = true;
  let theme: "light" | "dark" = "dark";
  let diagnosticRoute: Route | null = null;
  let diagnosticReport: DiagnosticReport | null = null;
  let diagnosticLoading = false;
  let diagnosticError = "";
  let diagnosticHistory: HealthSnapshot[] = [];
  let historyRetention = 288;
  let historyIntervalMs = 300000;
  let historyError = "";
  let initialEditorState = "";
  let deleteCandidate: Route | null = null;
  let discardEditorOpen = false;
  let openRouteMenuId: number | null = null;
  let pendingRouteIds = new Set<number>();
  let highlightedRouteId: number | null = null;
  let commandCopied = false;
  let drawerFirstInput: HTMLInputElement;
  let deleteCancelButton: HTMLButtonElement;
  let discardCancelButton: HTMLButtonElement;
  let browserProbe: BrowserProbe = {
    status: "idle",
    summary: "Browser probe has not run",
  };
  const commonWebPorts = [
    80, 8080, 3000, 8000, 5000, 5173, 4200, 8081, 3001, 8888, 443, 8443,
  ];

  async function refresh(showLoading = true) {
    if (showLoading) loading = true;
    try {
      const [containersResponse, routesResponse] = await Promise.all([
        fetch("/api/v1/containers"),
        fetch("/api/v1/routes"),
      ]);
      if (!containersResponse.ok)
        throw new Error(`Discovery failed (${containersResponse.status})`);
      if (!routesResponse.ok)
        throw new Error(`Route loading failed (${routesResponse.status})`);
      const containersPayload = await containersResponse.json();
      const routesPayload = await routesResponse.json();
      containers = containersPayload.containers;
      routes = routesPayload.routes;
      if (
        selected &&
        !editing &&
        !containers.some((container) => container.id === selected?.id)
      )
        closeEditor(true);
      baseDomain = routesPayload.baseDomain;
      reconcileEverySeconds = Math.max(
        1,
        Math.round((routesPayload.reconcileIntervalMs || 5000) / 1000),
      );
      const routeIds = new Set(routes.map((route) => route.id));
      routeReadiness = Object.fromEntries(
        Object.entries(routeReadiness).filter(([id]) =>
          routeIds.has(Number(id)),
        ),
      );
      for (const route of routes) void ensureReadiness(route);
      if (!showLoading) error = "";
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "Refresh failed";
    } finally {
      if (showLoading) loading = false;
    }
  }

  function readinessFor(route: Route): RouteReadiness {
    const readiness = routeReadiness[route.id];
    if (readiness?.revision === route.revision) return readiness;
    return {
      routeId: route.id,
      revision: route.revision,
      state: route.enabled ? "reconciling" : "disabled",
      ready: false,
      message: route.enabled
        ? "Checking whether Traefik has activated this route."
        : "Route is disabled and is not published to Traefik.",
      checkedAt: new Date().toISOString(),
    };
  }

  function wait(milliseconds: number) {
    return new Promise((resolve) => window.setTimeout(resolve, milliseconds));
  }

  async function ensureReadiness(route: Route) {
    if (readinessPolling.get(route.id) === route.revision) return;
    readinessPolling.set(route.id, route.revision);
    const deadline = Date.now() + 30_000;
    try {
      while (mounted) {
        const current = routes.find((candidate) => candidate.id === route.id);
        if (!current || current.revision !== route.revision) return;
        try {
          const response = await fetch(`/api/v1/routes/${route.id}/readiness`);
          const payload = await response.json();
          if (!response.ok) {
            throw new Error(
              payload.error || `Readiness check failed (${response.status})`,
            );
          }
          routeReadiness = { ...routeReadiness, [route.id]: payload };
          if (
            payload.ready ||
            payload.state === "disabled" ||
            payload.state === "error"
          ) {
            return;
          }
        } catch (cause) {
          routeReadiness = {
            ...routeReadiness,
            [route.id]: {
              routeId: route.id,
              revision: route.revision,
              state: "publishing",
              ready: false,
              message:
                cause instanceof Error
                  ? cause.message
                  : "Readiness check is temporarily unavailable.",
              checkedAt: new Date().toISOString(),
            },
          };
        }
        if (Date.now() >= deadline) {
          routeReadiness = {
            ...routeReadiness,
            [route.id]: {
              ...readinessFor(route),
              state: "error",
              ready: false,
              message:
                "Route activation is taking longer than 30 seconds. Open Diagnose for the failing layer.",
              checkedAt: new Date().toISOString(),
            },
          };
          return;
        }
        await wait(600);
      }
    } finally {
      if (readinessPolling.get(route.id) === route.revision) {
        readinessPolling.delete(route.id);
      }
    }
  }

  function choose(container: Container) {
    activeTab = "containers";
    selected = container;
    editing = null;
    routeName = availableRouteName(
      slug(container.composeService || container.name),
    );
    routePort = recommendedPort(container.exposedPorts);
    routeScheme = recommendedScheme(routePort);
    notice = "";
    commandCopied = false;
    initialEditorState = editorState();
    window.setTimeout(() => drawerFirstInput?.focus(), 0);
  }

  function normalizedSearch(value: string) {
    return value.trim().toLowerCase();
  }

  function filteredRoutes() {
    const query = normalizedSearch(routeSearch);
    if (!query) return routes;
    return routes.filter((route) =>
      [
        route.name,
        `${route.name}.${baseDomain}`,
        route.scheme,
        String(route.port),
        route.observed?.containerName || "",
        route.observed?.state || "",
      ].some((value) => value.toLowerCase().includes(query)),
    );
  }

  function filteredContainers() {
    const query = normalizedSearch(containerSearch);
    if (!query) return containers;
    return containers.filter((container) =>
      [
        container.name,
        container.image,
        container.status,
        container.composeProject || "",
        container.composeService || "",
        ...container.exposedPorts.map(String),
      ].some((value) => value.toLowerCase().includes(query)),
    );
  }

  function edit(route: Route) {
    activeTab = "routes";
    editing = route;
    selected =
      containers.find((container) => matches(route.selector, container)) || null;
    routeName = route.name;
    routePort = route.port;
    routeScheme = route.scheme;
    notice = "";
    commandCopied = false;
    initialEditorState = editorState();
    window.setTimeout(() => drawerFirstInput?.focus(), 0);
  }

  function editorState() {
    return JSON.stringify({
      name: routeName,
      port: Number(routePort),
      scheme: routeScheme,
    });
  }

  function closeEditor(force = false) {
    if (!force && initialEditorState && editorState() !== initialEditorState) {
      void requestDiscard();
      return;
    }
    selected = null;
    editing = null;
    initialEditorState = "";
    discardEditorOpen = false;
    commandCopied = false;
  }

  function showTab(tab: ActiveTab) {
    if (activeTab === tab) return;
    closeEditor();
    closeDiagnostics();
    openRouteMenuId = null;
    activeTab = tab;
  }

  function matches(selector: Selector, container: Container) {
    if (selector.containerId)
      return container.id.startsWith(selector.containerId);
    return (
      selector.composeProject === container.composeProject &&
      selector.composeService === container.composeService
    );
  }

  function slug(value: string) {
    return value
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
      .slice(0, 63)
      .replace(/-+$/g, "");
  }

  function availableRouteName(base: string) {
    const fallback = base || "app";
    const used = new Set(routes.map((route) => route.name));
    if (!used.has(fallback)) return fallback;
    for (let suffix = 2; ; suffix += 1) {
      const ending = `-${suffix}`;
      const candidate = `${fallback
        .slice(0, 63 - ending.length)
        .replace(/-+$/g, "")}${ending}`;
      if (!used.has(candidate)) return candidate;
    }
  }

  function uniquePorts(ports: number[]) {
    return [...new Set(ports.filter((port) => port > 0))].sort(
      (left, right) => left - right,
    );
  }

  function recommendedPort(ports: number[]) {
    const available = uniquePorts(ports);
    if (available.length === 1) return available[0];
    return commonWebPorts.find((port) => available.includes(port)) || 0;
  }

  function recommendedScheme(port: number) {
    return port === 443 || port === 8443 ? "https" : "http";
  }

  function nameConflict() {
    return routes.find(
      (route) => route.name === routeName && route.id !== editing?.id,
    );
  }

  function portRecommendation() {
    if (!selected) return "";
    const available = uniquePorts(selected.exposedPorts);
    if (available.length === 0) {
      return "This container declares no internal TCP port. Add `expose` to its Compose service, recreate it, then refresh Docker.";
    }
    if (routePort === 0) {
      return `Choose the app's web listener: ${available.map((port) => `:${port}`).join(", ")}.`;
    }
    if (!available.includes(Number(routePort))) {
      return `Port :${routePort} is not declared by this container. Available: ${available.map((port) => `:${port}`).join(", ")}.`;
    }
    if (available.length === 1) {
      return `Selected the container's only declared port, :${routePort}.`;
    }
    return `Suggested :${routePort} as the likely web listener. Other declared ports: ${available
      .filter((port) => port !== Number(routePort))
      .map((port) => `:${port}`)
      .join(", ")}.`;
  }

  function invalidSelectedPort() {
    if (!selected) return false;
    return !selected.exposedPorts.includes(Number(routePort));
  }

  function selectorFor(container: Container): Selector {
    if (container.composeProject && container.composeService) {
      return {
        composeProject: container.composeProject,
        composeService: container.composeService,
      };
    }
    return {
      composeProject: "",
      composeService: "",
      containerId: container.id,
    };
  }

  function commandFor() {
    if (editing) {
      return `docklane route edit ${editing.id} --name ${routeName} --port ${routePort} --scheme ${routeScheme}`;
    }
    if (!selected) return "";
    if (selected.composeProject && selected.composeService) {
      return `docklane route add ${routeName} --project ${selected.composeProject} --service ${selected.composeService} --port ${routePort} --scheme ${routeScheme}`;
    }
    return `docklane route add ${routeName} --container ${selected.id.slice(0, 12)} --port ${routePort} --scheme ${routeScheme}`;
  }

  async function copyCommand() {
    try {
      await navigator.clipboard.writeText(commandFor());
      commandCopied = true;
      window.setTimeout(() => (commandCopied = false), 1800);
    } catch {
      error = "Clipboard access was unavailable";
    }
  }

  function setRoutePending(routeId: number, pending: boolean) {
    const next = new Set(pendingRouteIds);
    if (pending) next.add(routeId);
    else next.delete(routeId);
    pendingRouteIds = next;
  }

  function readyRouteCount() {
    return routes.filter((route) => readinessFor(route).ready).length;
  }

  function publishingRouteCount() {
    return routes.filter((route) =>
      ["reconciling", "publishing", "verifying"].includes(
        readinessFor(route).state,
      ),
    ).length;
  }

  function attentionRouteCount() {
    return routes.filter(
      (route) =>
        readinessFor(route).state === "error" ||
        ["unresolved", "ambiguous", "error"].includes(
          route.observed?.state ?? "",
        ),
    ).length;
  }

  function handleModalKeydown(event: KeyboardEvent) {
    if (event.key !== "Tab") return;
    const modal = event.currentTarget as HTMLElement;
    const focusable = Array.from(
      modal.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), [href]',
      ),
    );
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  async function requestDelete(route: Route) {
    openRouteMenuId = null;
    deleteCandidate = route;
    await tick();
    deleteCancelButton?.focus();
  }

  async function requestDiscard() {
    discardEditorOpen = true;
    await tick();
    discardCancelButton?.focus();
  }

  function writableRoute(route: Route, enabled = route.enabled) {
    return {
      revision: route.revision,
      name: route.name,
      selector: route.selector,
      port: route.port,
      scheme: route.scheme,
      enabled,
    };
  }

  async function saveRoute() {
    if (!selected && !editing) return;
    saving = true;
    error = "";
    notice = "";
    const selector = selected
      ? selectorFor(selected)
      : (editing as Route).selector;
    const routeId = editing?.id;
    try {
      const response = await fetch(
        routeId ? `/api/v1/routes/${routeId}` : "/api/v1/routes",
        {
          method: routeId ? "PUT" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            ...(editing ? { revision: editing.revision } : {}),
            name: routeName,
            selector,
            port: Number(routePort),
            scheme: routeScheme,
            enabled: editing?.enabled ?? true,
          }),
        },
      );
      const payload = await response.json();
      if (!response.ok)
        throw new Error(payload.error || `Save failed (${response.status})`);
      highlightedRouteId = payload.id || routeId || null;
      notice = `${routeId ? "Updated" : "Created"} ${routeName}.${baseDomain} · publishing route…`;
      closeEditor(true);
      activeTab = "routes";
      await refresh(false);
      window.setTimeout(() => (highlightedRouteId = null), 3200);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "Route save failed";
    } finally {
      saving = false;
    }
  }

  async function toggle(route: Route) {
    error = "";
    openRouteMenuId = null;
    setRoutePending(route.id, true);
    try {
      const response = await fetch(`/api/v1/routes/${route.id}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(writableRoute(route, !route.enabled)),
      });
      const payload = await response.json();
      if (!response.ok) {
        error = payload.error || `Update failed (${response.status})`;
        return;
      }
      notice = `${route.enabled ? "Disabled" : "Enabled"} ${route.name}`;
      await refresh(false);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "Route update failed";
    } finally {
      setRoutePending(route.id, false);
    }
  }

  async function remove(route: Route) {
    error = "";
    setRoutePending(route.id, true);
    try {
      const response = await fetch(`/api/v1/routes/${route.id}`, {
        method: "DELETE",
      });
      if (!response.ok) {
        const payload = await response.json();
        error = payload.error || `Delete failed (${response.status})`;
        return;
      }
      if (editing?.id === route.id) closeEditor(true);
      if (diagnosticRoute?.id === route.id) closeDiagnostics();
      notice = `Deleted ${route.name}.${baseDomain}`;
      deleteCandidate = null;
      await refresh(false);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "Route deletion failed";
    } finally {
      setRoutePending(route.id, false);
    }
  }

  async function diagnose(route: Route) {
    activeTab = "routes";
    diagnosticRoute = route;
    diagnosticReport = null;
    diagnosticError = "";
    diagnosticHistory = [];
    historyError = "";
    diagnosticLoading = true;
    browserProbe = {
      status: "pending",
      summary: "Connecting from this browser…",
    };
    await Promise.allSettled([
      loadControllerDiagnostics(route).then(() => loadDiagnosticHistory(route)),
      probeFromBrowser(route),
    ]);
    diagnosticLoading = false;
  }

  async function loadControllerDiagnostics(route: Route) {
    try {
      const response = await fetch(`/api/v1/diagnostics/routes/${route.id}`, {
        cache: "no-store",
      });
      const payload = await response.json();
      if (!response.ok)
        throw new Error(
          payload.error || `Diagnostics failed (${response.status})`,
        );
      diagnosticReport = payload;
    } catch (cause) {
      diagnosticError =
        cause instanceof Error ? cause.message : "Diagnostics failed";
    }
  }

  async function probeFromBrowser(route: Route) {
    const hostname = `${route.name}.${baseDomain}`;
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 7000);
    try {
      await fetch(`https://${hostname}/`, {
        method: "GET",
        mode: "no-cors",
        cache: "no-store",
        signal: controller.signal,
      });
      browserProbe = {
        status: "pass",
        summary: "Browser accepted HTTPS and reached the route",
        detail:
          "This opaque probe verifies connection and certificate acceptance; application status remains controller-observed.",
      };
    } catch (cause) {
      browserProbe = {
        status: "fail",
        summary: "Browser could not reach the HTTPS route",
        detail:
          cause instanceof Error
            ? cause.message
            : "Check local DNS and certificate trust in this browser.",
      };
    } finally {
      window.clearTimeout(timeout);
    }
  }

  async function loadDiagnosticHistory(route: Route) {
    try {
      const response = await fetch(
        `/api/v1/diagnostics/routes/${route.id}/history?limit=50`,
        { cache: "no-store" },
      );
      const payload = await response.json();
      if (!response.ok)
        throw new Error(
          payload.error || `History loading failed (${response.status})`,
        );
      diagnosticHistory = payload.snapshots;
      historyRetention = payload.retention;
      historyIntervalMs = payload.sampleIntervalMs;
    } catch (cause) {
      historyError =
        cause instanceof Error ? cause.message : "History loading failed";
    }
  }

  function groupedChecks(checks: DiagnosticCheck[]) {
    const groups = new Map<string, DiagnosticCheck[]>();
    for (const check of checks) {
      const entries = groups.get(check.layer) || [];
      entries.push(check);
      groups.set(check.layer, entries);
    }
    return Array.from(groups, ([layer, entries]) => ({ layer, entries }));
  }

  function chronologicalHistory() {
    return [...diagnosticHistory].reverse();
  }

  function historyCount(status: DiagnosticStatus) {
    return diagnosticHistory.filter((snapshot) => snapshot.status === status)
      .length;
  }

  function intervalLabel(milliseconds: number) {
    const minutes = Math.round(milliseconds / 60000);
    return minutes === 1 ? "1 minute" : `${minutes} minutes`;
  }

  async function copyDiagnostics() {
    if (!diagnosticRoute) return;
    try {
      await navigator.clipboard.writeText(
        JSON.stringify(
          { controller: diagnosticReport, browser: browserProbe },
          null,
          2,
        ),
      );
      notice = `Copied diagnostics for ${diagnosticRoute.name}`;
    } catch {
      diagnosticError = "Clipboard access was unavailable";
    }
  }

  function closeDiagnostics() {
    diagnosticRoute = null;
    diagnosticReport = null;
    diagnosticError = "";
    diagnosticHistory = [];
    historyError = "";
    browserProbe = { status: "idle", summary: "Browser probe has not run" };
  }

  function refreshDiagnostics() {
    if (diagnosticRoute) diagnose(diagnosticRoute);
  }

  function applyTheme(next: "light" | "dark") {
    theme = next;
    document.documentElement.dataset.theme = next;
    localStorage.setItem("docklane-theme", next);
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute("content", next === "dark" ? "#0a100d" : "#f4f7f5");
  }

  onMount(() => {
    mounted = true;
    theme =
      document.documentElement.dataset.theme === "light" ? "light" : "dark";
    applyTheme(theme);
    refresh();
    const timer = window.setInterval(() => refresh(false), 5000);
    return () => {
      mounted = false;
      window.clearInterval(timer);
    };
  });
</script>

<svelte:head>
  <title>Docklane · Local container gateway</title>
</svelte:head>

<svelte:body class:modal-open={!!selected || !!editing || !!deleteCandidate || discardEditorOpen} />

<main>
  <nav class="product-nav" aria-label="Primary navigation">
    <a
      class="brand"
      href="/"
      aria-label="Docklane routes"
      onclick={(event) => {
        event.preventDefault();
        showTab("routes");
      }}
    >
      <img src={logoUrl} alt="" />
      <span>Docklane</span>
    </a>
    <div class="product-tabs">
      <button
        type="button"
        class:active={activeTab === "routes"}
        aria-current={activeTab === "routes" ? "page" : undefined}
        onclick={() => showTab("routes")}
      >
        Routes
        <span>{routes.length}</span>
      </button>
      <button
        type="button"
        class:active={activeTab === "containers"}
        aria-current={activeTab === "containers" ? "page" : undefined}
        onclick={() => showTab("containers")}
      >
        Containers
        <span>{containers.length}</span>
      </button>
    </div>
    <div class="product-nav-meta">
      <span class="local-only">Local controller</span>
      <button
        class="theme-toggle"
        type="button"
        aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}
        title={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}
        onclick={() => applyTheme(theme === "dark" ? "light" : "dark")}
      >
        {#if theme === "dark"}
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <circle cx="12" cy="12" r="3.5"></circle>
            <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"></path>
          </svg>
        {:else}
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M20 15.2A8.3 8.3 0 0 1 8.8 4a8.3 8.3 0 1 0 11.2 11.2Z"></path>
          </svg>
        {/if}
      </button>
    </div>
  </nav>

  {#if notice}
    <div class="toast success" role="status" aria-live="polite">
      <span>{notice}</span>
      <button type="button" aria-label="Dismiss notification" onclick={() => (notice = "")}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"></path></svg>
      </button>
    </div>
  {/if}
  {#if error}
    <div class="toast error" role="alert" aria-live="assertive">
      <span>{error}</span>
      <button type="button" aria-label="Dismiss error" onclick={() => (error = "")}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"></path></svg>
      </button>
    </div>
  {/if}

  {#if activeTab === "routes"}
  {#if !diagnosticRoute}
  <section aria-labelledby="routes-title">
    <header class="page-header">
      <div>
        <p class="eyebrow">LOCAL ROUTING</p>
        <h1 id="routes-title">Routes</h1>
        <p>Stable HTTPS names for local container workloads.</p>
      </div>
      <button class="primary-action" type="button" onclick={() => showTab("containers")}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"></path></svg>
        New route
      </button>
    </header>

    <div class="route-summary" aria-label="Route status summary">
      <div><strong>{routes.length}</strong><span>Total routes</span></div>
      <div><i class="summary-dot ready"></i><strong>{readyRouteCount()}</strong><span>Ready</span></div>
      <div><i class="summary-dot publishing"></i><strong>{publishingRouteCount()}</strong><span>Publishing</span></div>
      <div><i class="summary-dot attention"></i><strong>{attentionRouteCount()}</strong><span>Needs attention</span></div>
    </div>

    <div class="list-toolbar">
      <div class="title-with-count">
        <h2>All routes</h2>
        <span>
          {#if routeSearch}
            {filteredRoutes().length} of {routes.length}
          {:else}
            syncs every {reconcileEverySeconds}s
          {/if}
        </span>
      </div>
      <div class="panel-toolbar">
        <label class="search-field">
          <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="6"></circle><path d="m16 16 4 4"></path></svg>
          <input
            type="search"
            bind:value={routeSearch}
            placeholder="Search routes"
            aria-label="Search routes"
          />
          {#if routeSearch}
            <button class="clear-search" type="button" aria-label="Clear route search" onclick={() => (routeSearch = "")}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m7 7 10 10M17 7 7 17"></path></svg>
            </button>
          {/if}
        </label>
        <button class="icon-button secondary" type="button" aria-label="Refresh routes" title="Refresh routes" onclick={() => refresh()}>
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6v5h-5M4 18v-5h5"></path><path d="M6.1 9A7 7 0 0 1 18.7 7.3L20 11M4 13l1.3 3.7A7 7 0 0 0 17.9 15"></path></svg>
        </button>
      </div>
    </div>
    {#if routes.length === 0}
      <div class="empty">
        <span class="empty-icon">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 7h14M5 12h14M5 17h8"></path></svg>
        </span>
        <h3>No routes yet</h3>
        <p>Choose a running container and give it a stable local hostname.</p>
        <button type="button" onclick={() => showTab("containers")}>Browse containers</button>
      </div>
    {:else if filteredRoutes().length === 0}
      <div class="empty compact">
        No routes match “{routeSearch}”.
      </div>
    {:else}
      <div class="route-list">
        <div class="route-list-head" aria-hidden="true">
          <span>Status</span><span>Hostname</span><span>Workload</span><span>Upstream</span><span></span>
        </div>
        {#each filteredRoutes() as route}
          {@const availability = readinessFor(route)}
          <div class:highlighted={highlightedRouteId === route.id} class="route-row">
            <div class="route-status-cell">
              <span
                class={`route-state ${availability.state}`}
                title={availability.message}
              ></span>
              <span class="mobile-label">{availability.state}</span>
            </div>
            <div class="route-name">
              {#if availability.ready}
                <a href={`https://${route.name}.${baseDomain}`} target="_blank" rel="noreferrer">
                  {route.name}.{baseDomain}
                  <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 5h5v5M19 5l-8 8"></path><path d="M18 13v5H6V6h5"></path></svg>
                </a>
              {:else}
                <span
                  class="route-link-pending"
                  title="The link unlocks after Traefik confirms the route."
                >
                  {route.name}.{baseDomain}
                </span>
              {/if}
              <small>
                {availability.state}
                {#if !availability.ready}
                  · {availability.message}
                {/if}
              </small>
            </div>
            <div class="route-workload">
              <strong>{route.selector.composeService || route.observed?.containerName || "Container"}</strong>
              <small>{route.selector.composeProject || route.observed?.containerName || "Direct selector"}</small>
            </div>
            <code class="route-upstream">{route.scheme}://:{route.port}</code>
            <div class="route-actions">
              <button class="ghost small" onclick={() => diagnose(route)}>
                Diagnose
              </button>
              <button
                class="icon-button ghost small"
                type="button"
                aria-label={`More actions for ${route.name}`}
                aria-expanded={openRouteMenuId === route.id}
                onclick={() => (openRouteMenuId = openRouteMenuId === route.id ? null : route.id)}
                disabled={pendingRouteIds.has(route.id)}
              >
                {#if pendingRouteIds.has(route.id)}
                  <span class="spinner"></span>
                {:else}
                  <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="5" cy="12" r="1"></circle><circle cx="12" cy="12" r="1"></circle><circle cx="19" cy="12" r="1"></circle></svg>
                {/if}
              </button>
              {#if openRouteMenuId === route.id}
                <div class="action-menu">
                  <button type="button" onclick={() => { openRouteMenuId = null; edit(route); }}>Edit route</button>
                  <button type="button" onclick={() => toggle(route)}>
                    {route.enabled ? "Disable route" : "Enable route"}
                  </button>
                  <span></span>
                  <button class="danger" type="button" onclick={() => requestDelete(route)}>
                    Delete route
                  </button>
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </section>
  {/if}

  {#if diagnosticRoute}
    <section class="diagnostics" aria-labelledby="diagnostics-title">
      <button class="back-link" type="button" onclick={closeDiagnostics}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m15 18-6-6 6-6"></path></svg>
        Back to routes
      </button>
      <div class="diagnostics-header">
        <div>
          <p class="eyebrow">ROUTE DIAGNOSTICS</p>
          <h2 id="diagnostics-title">
            {diagnosticRoute.name}.{baseDomain}
          </h2>
          <p>
            Controller and browser checks are separate because they use
            different DNS, network, and certificate trust contexts.
          </p>
        </div>
        <div class="diagnostics-actions">
          <button
            class="secondary small"
            onclick={refreshDiagnostics}
            disabled={diagnosticLoading}
          >
            {diagnosticLoading ? "Checking…" : "Refresh checks"}
          </button>
          <button class="ghost small" onclick={copyDiagnostics}>Copy JSON</button>
        </div>
      </div>

      {#if diagnosticError}
        <div class="diagnostic-error">{diagnosticError}</div>
      {/if}

      <div class="perspective browser-perspective">
        <div>
          <span class="perspective-label">Browser perspective</span>
          <strong>{browserProbe.summary}</strong>
          {#if browserProbe.detail}<small>{browserProbe.detail}</small>{/if}
        </div>
        <span class={`status-pill ${browserProbe.status}`}>
          {browserProbe.status}
        </span>
      </div>

      {#if diagnosticReport}
        <div class="diagnostic-summary">
          <span class={`status-pill ${diagnosticReport.status}`}>
            {diagnosticReport.status}
          </span>
          <span>
            Controller perspective ·
            {new Date(diagnosticReport.generatedAt).toLocaleTimeString()}
          </span>
        </div>
        <div class="health-history">
          <div class="history-heading">
            <div>
              <span class="perspective-label">Controller health history</span>
              <strong>
                {diagnosticHistory.length} recent snapshot{diagnosticHistory.length ===
                1
                  ? ""
                  : "s"}
              </strong>
            </div>
            <small>
              Every {intervalLabel(historyIntervalMs)} · retains
              {historyRetention} per route
            </small>
          </div>
          {#if historyError}
            <p class="history-error">{historyError}</p>
          {:else if diagnosticHistory.length > 0}
            <div class="history-timeline" aria-label="Recent controller health">
              {#each chronologicalHistory() as snapshot}
                <span
                  class={`history-point ${snapshot.status}`}
                  title={`${new Date(snapshot.recordedAt).toLocaleString()} · ${snapshot.status}`}
                  aria-label={`${snapshot.status} at ${new Date(snapshot.recordedAt).toLocaleString()}`}
                ></span>
              {/each}
            </div>
            <div class="history-legend">
              <span><i class="pass"></i>{historyCount("pass")} pass</span>
              <span><i class="warn"></i>{historyCount("warn")} warning</span>
              <span><i class="fail"></i>{historyCount("fail")} fail</span>
            </div>
          {:else}
            <p class="history-empty">
              The first snapshot will appear after this diagnostic completes.
            </p>
          {/if}
        </div>
        <div class="diagnostic-groups">
          {#each groupedChecks(diagnosticReport.checks) as group}
            <div class="diagnostic-group">
              <h3>{group.layer}</h3>
              {#each group.entries as check}
                <div class="diagnostic-check">
                  <span class={`check-mark ${check.status}`}>
                    {check.status === "pass"
                      ? "✓"
                      : check.status === "warn"
                        ? "!"
                        : "×"}
                  </span>
                  <div>
                    <strong>{check.summary}</strong>
                    {#if check.detail}<p>{check.detail}</p>{/if}
                    {#if check.suggestion}
                      <p class="repair">Repair: {check.suggestion}</p>
                    {/if}
                  </div>
                </div>
              {/each}
            </div>
          {/each}
        </div>
      {:else if diagnosticLoading}
        <div class="empty compact">Inspecting route layers…</div>
      {/if}
    </section>
  {/if}

  {:else if activeTab === "containers"}
  <section aria-labelledby="containers-title">
    <header class="page-header">
      <div>
        <p class="eyebrow">DOCKER DISCOVERY</p>
        <h1 id="containers-title">Containers</h1>
        <p>Running workloads available to the local gateway.</p>
      </div>
      <button class="secondary" type="button" onclick={() => refresh()}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6v5h-5M4 18v-5h5"></path><path d="M6.1 9A7 7 0 0 1 18.7 7.3L20 11M4 13l1.3 3.7A7 7 0 0 0 17.9 15"></path></svg>
        Refresh Docker
      </button>
    </header>
    <div class="list-toolbar">
      <div class="title-with-count">
        <h2>Running workloads</h2>
        <span>
          {#if containerSearch}
            {filteredContainers().length} of {containers.length}
          {:else}
            {containers.length} discovered
          {/if}
        </span>
      </div>
      <div class="container-toolbar">
        <label class="search-field">
          <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="6"></circle><path d="m16 16 4 4"></path></svg>
          <input
            type="search"
            bind:value={containerSearch}
            placeholder="Search containers"
            aria-label="Search containers"
          />
          {#if containerSearch}
            <button class="clear-search" type="button" aria-label="Clear container search" onclick={() => (containerSearch = "")}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m7 7 10 10M17 7 7 17"></path></svg>
            </button>
          {/if}
        </label>
      </div>
    </div>

    {#if loading}
      <div class="empty">Inspecting Docker…</div>
    {:else if containers.length === 0}
      <div class="empty">No running containers found.</div>
    {:else if filteredContainers().length === 0}
      <div class="empty compact">
        No containers match “{containerSearch}”.
      </div>
    {:else}
      <div class="container-table-scroll">
        <div class="container-table">
          <div class="container-table-head" aria-hidden="true">
            <span>Workload</span>
            <span class="image-column">Image</span>
            <span>Ports</span>
            <span class="state-column">State</span>
            <span></span>
          </div>
          {#each filteredContainers() as container}
            {@const managed = container.systemRole === "reverse-proxy"}
            <div
              class:managed
              class="container-table-row"
              title={managed
                ? "The active reverse proxy is managed by Docklane and cannot route to itself."
                : undefined}
            >
              <span class="workload-cell">
                <strong>{container.composeService || container.name}</strong>
                <small>{container.name}</small>
              </span>
              <span class="image-cell image-column">
                {container.image}
              </span>
              <span class="ports-cell">
                {#each uniquePorts(container.exposedPorts) as port}
                  <code>:{port}</code>
                {:else}
                  <small>None declared</small>
                {/each}
              </span>
              <span class="container-state state-column">
                <i class="status-dot" aria-hidden="true"></i>
                {#if managed}
                  <span class="system-badge">gateway</span>
                {:else}
                  <span>{container.status}</span>
                {/if}
              </span>
              <span class="container-action">
                {#if managed}
                  <span class="managed-note">System workload</span>
                {:else}
                  <button class="secondary small" type="button" onclick={() => choose(container)}>
                    Create route
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 18 6-6-6-6"></path></svg>
                  </button>
                {/if}
              </span>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </section>
  {/if}
</main>

{#if selected || editing}
  <div class="drawer-layer">
    <button class="drawer-backdrop" type="button" aria-label="Close route editor" onclick={() => closeEditor()}></button>
    <div
      class="route-drawer"
      role="dialog"
      aria-modal="true"
      aria-labelledby="route-editor-title"
      tabindex="-1"
      onkeydown={(event) => {
        if (event.key === "Escape") closeEditor();
        handleModalKeydown(event);
      }}
    >
      <div class="drawer-header">
        <div>
          <p class="eyebrow">{editing ? "EDIT ROUTE" : "NEW ROUTE"}</p>
          <h2 id="route-editor-title">
            {editing ? `${editing.name}.${baseDomain}` : selected?.composeService || selected?.name}
          </h2>
        </div>
        <button class="icon-button ghost" type="button" aria-label="Close route editor" onclick={() => closeEditor()}>
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"></path></svg>
        </button>
      </div>

      <div class="drawer-workload">
        <span>WORKLOAD</span>
        <strong>{selected?.composeService || selected?.name || editing?.selector.composeService || "Container"}</strong>
        <small>
          {#if selected?.composeProject}
            {selected.composeProject} / {selected.name}
          {:else if selected}
            {selected.name}
          {:else}
            Stored route selector
          {/if}
        </small>
      </div>

      <form
        class="drawer-form"
        onsubmit={(event) => {
          event.preventDefault();
          saveRoute();
        }}
      >
        <label>
          Local hostname
          <div class="domain-input">
            <input
              bind:this={drawerFirstInput}
              bind:value={routeName}
              required
              pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?"
              aria-describedby="route-name-hint"
            />
            <span>.{baseDomain}</span>
          </div>
          {#if nameConflict()}
            <small id="route-name-hint" class="field-hint error">
              Already used by route #{nameConflict()?.id}. Choose another name.
            </small>
          {:else}
            <small id="route-name-hint" class="field-hint">
              Browser URL: https://{routeName || "name"}.{baseDomain}
            </small>
          {/if}
        </label>

        <div class="field-row">
          <label>
            Internal port
            <input
              type="number"
              min="1"
              max="65535"
              list="declared-container-ports"
              bind:value={routePort}
              onchange={() => {
                if (!editing) routeScheme = recommendedScheme(Number(routePort));
              }}
              required
            />
            {#if selected}
              <datalist id="declared-container-ports">
                {#each uniquePorts(selected.exposedPorts) as port}
                  <option value={port}></option>
                {/each}
              </datalist>
            {/if}
          </label>
          <label>
            Protocol
            <select bind:value={routeScheme}>
              <option value="http">HTTP</option>
              <option value="https">HTTPS</option>
            </select>
          </label>
        </div>
        {#if selected}
          <small class:error={routePort === 0 || invalidSelectedPort()} class="field-hint port-hint">
            {portRecommendation()}
          </small>
        {:else}
          <small class="field-hint port-hint">
            This is the protocol and port Traefik uses inside Docker. Browser access remains HTTPS.
          </small>
        {/if}

        <div class="command-preview">
          <div>
            <span>EQUIVALENT CLI</span>
            <code>{commandFor()}</code>
          </div>
          <button class="icon-button ghost" type="button" aria-label="Copy CLI command" title="Copy CLI command" onclick={copyCommand}>
            {#if commandCopied}
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12 4 4L19 6"></path></svg>
            {:else}
              <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="8" y="8" width="11" height="11" rx="2"></rect><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"></path></svg>
            {/if}
          </button>
        </div>

        <div class="drawer-actions">
          <button type="button" class="secondary" onclick={() => closeEditor()}>Cancel</button>
          <button
            type="submit"
            disabled={saving || !!nameConflict() || routePort < 1 || invalidSelectedPort()}
          >
            {saving ? "Saving…" : editing ? "Save changes" : "Create route"}
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}

{#if deleteCandidate}
  <div
    class="modal-layer"
    role="presentation"
    onkeydown={(event) => {
      if (event.key === "Escape") deleteCandidate = null;
      handleModalKeydown(event);
    }}
  >
    <button class="modal-backdrop" type="button" aria-label="Cancel deletion" onclick={() => (deleteCandidate = null)}></button>
    <div class="confirm-dialog" role="alertdialog" aria-modal="true" aria-labelledby="delete-title" aria-describedby="delete-description">
      <span class="danger-icon">
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 8v5M12 17h.01"></path><path d="M10.3 4.9 3.6 17a2 2 0 0 0 1.8 3h13.2a2 2 0 0 0 1.8-3L13.7 4.9a2 2 0 0 0-3.4 0Z"></path></svg>
      </span>
      <h2 id="delete-title">Delete route?</h2>
      <p id="delete-description">
        <strong>{deleteCandidate.name}.{baseDomain}</strong> will stop resolving through Docklane. The container is not changed.
      </p>
      <div class="confirm-actions">
        <button bind:this={deleteCancelButton} class="secondary" type="button" onclick={() => (deleteCandidate = null)}>Cancel</button>
        <button class="danger-solid" type="button" disabled={pendingRouteIds.has(deleteCandidate.id)} onclick={() => remove(deleteCandidate as Route)}>
          {pendingRouteIds.has(deleteCandidate.id) ? "Deleting…" : "Delete route"}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if discardEditorOpen}
  <div
    class="modal-layer"
    role="presentation"
    onkeydown={(event) => {
      if (event.key === "Escape") discardEditorOpen = false;
      handleModalKeydown(event);
    }}
  >
    <button class="modal-backdrop" type="button" aria-label="Keep editing" onclick={() => (discardEditorOpen = false)}></button>
    <div class="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="discard-title">
      <h2 id="discard-title">Discard unsaved changes?</h2>
      <p>Your route configuration has changed. Closing now will lose those edits.</p>
      <div class="confirm-actions">
        <button bind:this={discardCancelButton} class="secondary" type="button" onclick={() => (discardEditorOpen = false)}>Keep editing</button>
        <button class="danger-solid" type="button" onclick={() => closeEditor(true)}>Discard changes</button>
      </div>
    </div>
  </div>
{/if}
