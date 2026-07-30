<script lang="ts">
  import { onMount } from "svelte";
  import logoUrl from "../../brand/logo-mark.svg";

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
    selected = container;
    editing = null;
    routeName = availableRouteName(
      slug(container.composeService || container.name),
    );
    routePort = recommendedPort(container.exposedPorts);
    routeScheme = recommendedScheme(routePort);
    notice = "";
  }

  function edit(route: Route) {
    editing = route;
    selected =
      containers.find((container) => matches(route.selector, container)) || null;
    routeName = route.name;
    routePort = route.port;
    routeScheme = route.scheme;
    notice = "";
  }

  function closeEditor() {
    selected = null;
    editing = null;
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
      notice = `${routeId ? "Updated" : "Created"} ${routeName}.${baseDomain} · publishing route…`;
      closeEditor();
      await refresh(false);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "Route save failed";
    } finally {
      saving = false;
    }
  }

  async function toggle(route: Route) {
    error = "";
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
  }

  async function remove(route: Route) {
    if (!confirm(`Delete the route ${route.name}.${baseDomain}?`)) return;
    error = "";
    const response = await fetch(`/api/v1/routes/${route.id}`, {
      method: "DELETE",
    });
    if (!response.ok) {
      const payload = await response.json();
      error = payload.error || `Delete failed (${response.status})`;
      return;
    }
    if (editing?.id === route.id) closeEditor();
    if (diagnosticRoute?.id === route.id) closeDiagnostics();
    notice = `Deleted ${route.name}.${baseDomain}`;
    await refresh(false);
  }

  async function diagnose(route: Route) {
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

<main>
  <nav class="product-nav" aria-label="Product">
    <a class="brand" href="/" aria-label="Docklane home">
      <img src={logoUrl} alt="" />
      <span>Docklane</span>
    </a>
    <div class="product-nav-meta">
      <span class="local-only">Local controller</span>
      <button
        class="theme-toggle"
        type="button"
        aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}
        title={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}
        onclick={() => applyTheme(theme === "dark" ? "light" : "dark")}
      >
        {theme === "dark" ? "☀" : "☾"}
      </button>
    </div>
  </nav>
  <header>
    <div>
      <p class="eyebrow">LOCAL CONTAINER GATEWAY</p>
      <h1>Your containers deserve names, not port numbers.</h1>
      <p class="lede">
        Discover HTTP services, assign a local domain, and let Traefik handle
        the route.
      </p>
    </div>
    <button class="refresh-action" onclick={() => refresh()}>Refresh Docker</button>
  </header>

  {#if notice}
    <div class="notice">{notice}</div>
  {/if}
  {#if error}
    <div class="notice error">{error}</div>
  {/if}

  {#if selected || editing}
    <section class="route-editor" aria-labelledby="route-editor-title">
      <div>
        <p class="eyebrow">{editing ? "EDIT ROUTE" : "NEW ROUTE"}</p>
        <h2 id="route-editor-title">
          {editing?.name || selected?.composeService || selected?.name}
        </h2>
        <p>
          Configure the workload's internal listener. No application host port
          is required.
        </p>
      </div>
      <form
        onsubmit={(event) => {
          event.preventDefault();
          saveRoute();
        }}
      >
        <label>
          Local name
          <div class="domain-input">
            <input
              bind:value={routeName}
              required
              pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?"
            />
            <span>.{baseDomain}</span>
          </div>
          {#if nameConflict()}
            <small class="field-hint error">
              This hostname already belongs to route #{nameConflict()?.id}.
              Choose another local name.
            </small>
          {:else if !editing}
            <small class="field-hint">
              Available hostname selected automatically.
            </small>
          {/if}
        </label>
        <div class="field-row">
          <label>
            Container port
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
              <small
                class:error={routePort === 0 || invalidSelectedPort()}
                class="field-hint"
              >
                {portRecommendation()}
              </small>
            {/if}
          </label>
          <label>
            Upstream scheme
            <select bind:value={routeScheme}>
              <option value="http">HTTP</option>
              <option value="https">HTTPS</option>
            </select>
          </label>
        </div>
        <div class="form-actions">
          <button
            type="submit"
            disabled={saving ||
              !!nameConflict() ||
              routePort < 1 ||
              invalidSelectedPort()}
          >
            {saving ? "Saving…" : editing ? "Save changes" : "Create route"}
          </button>
          <button type="button" class="secondary" onclick={closeEditor}>
            Cancel
          </button>
        </div>
        <code>{commandFor()}</code>
      </form>
    </section>
  {/if}

  <section aria-labelledby="routes-title">
    <div class="section-title">
      <h2 id="routes-title">Routes</h2>
      <span>{routes.length} saved · reconciles every {reconcileEverySeconds}s</span>
    </div>
    {#if routes.length === 0}
      <div class="empty compact">No routes yet. Choose a container below.</div>
    {:else}
      <div class="route-list">
        {#each routes as route}
          {@const availability = readinessFor(route)}
          <div class="route-row">
            <span
              class={`route-state ${availability.state}`}
              title={availability.message}
            ></span>
            <div class="route-name">
              {#if availability.ready}
                <a href={`https://${route.name}.${baseDomain}`} target="_blank">
                  {route.name}.{baseDomain}
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
                {#if route.observed?.containerName}
                  · {route.observed.containerName}
                {/if}
                {#if !availability.ready}
                  · {availability.message}
                {/if}
              </small>
            </div>
            <code>{route.scheme} · :{route.port}</code>
            <div class="route-actions">
              <button class="ghost small" onclick={() => diagnose(route)}>
                Diagnose
              </button>
              <button class="ghost small" onclick={() => edit(route)}>Edit</button>
              <button class="ghost small" onclick={() => toggle(route)}>
                {route.enabled ? "Disable" : "Enable"}
              </button>
              <button class="ghost danger small" onclick={() => remove(route)}>
                Delete
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </section>

  {#if diagnosticRoute}
    <section class="diagnostics" aria-labelledby="diagnostics-title">
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
          <button class="ghost small" onclick={closeDiagnostics}>Close</button>
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

  <section aria-labelledby="containers-title">
    <div class="section-title">
      <h2 id="containers-title">Containers</h2>
      <span>{containers.length} discovered</span>
    </div>

    {#if loading}
      <div class="empty">Inspecting Docker…</div>
    {:else if containers.length === 0}
      <div class="empty">No running containers found.</div>
    {:else}
      <div class="grid">
        {#each containers as container}
          <article>
            <div class="status-dot" title={container.status}></div>
            <div class="card-body">
              <h3>{container.composeService || container.name}</h3>
              <p>{container.image}</p>
              <div class="meta">
                {#if container.systemRole === "reverse-proxy"}
                  <span class="system-badge">gateway</span>
                {/if}
                {#each container.exposedPorts as port}
                  <span>:{port}</span>
                {:else}
                  <span>no declared HTTP port</span>
                {/each}
              </div>
            </div>
            {#if container.systemRole === "reverse-proxy"}
              <span
                class="system-action"
                title="The active reverse proxy cannot route to itself. Its dashboard uses api@internal."
              >
                Managed system container
              </span>
            {:else}
              <button class="secondary" onclick={() => choose(container)}>
                Create route
              </button>
            {/if}
          </article>
        {/each}
      </div>
    {/if}
  </section>
</main>
