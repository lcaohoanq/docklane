import type { Container, Route, RouteReadiness, Selector } from "./types";

const commonWebPorts = [80, 8080, 3000, 8000, 5000, 5173, 4200, 8081, 3001, 8888, 443, 8443];

export function filterRoutes(routes: Route[], baseDomain: string, search: string) {
  const query = search.trim().toLowerCase();
  if (!query) return routes;
  return routes.filter((route) =>
    [route.name, `${route.name}.${baseDomain}`, route.scheme, String(route.port), route.observed?.containerName || "", route.observed?.state || ""]
      .some((value) => value.toLowerCase().includes(query)),
  );
}

export function matchesSelector(selector: Selector, container: Container) {
  if (selector.containerId) return container.id.startsWith(selector.containerId);
  return selector.composeProject === container.composeProject && selector.composeService === container.composeService;
}

export function selectorFor(container: Container): Selector {
  if (container.composeProject && container.composeService) {
    return { composeProject: container.composeProject, composeService: container.composeService };
  }
  return { composeProject: "", composeService: "", containerId: container.id };
}

export function slug(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 63).replace(/-+$/g, "");
}

export function availableRouteName(base: string, routes: Route[]) {
  const fallback = base || "app";
  const used = new Set(routes.map((route) => route.name));
  if (!used.has(fallback)) return fallback;
  for (let suffix = 2; ; suffix += 1) {
    const ending = `-${suffix}`;
    const candidate = `${fallback.slice(0, 63 - ending.length).replace(/-+$/g, "")}${ending}`;
    if (!used.has(candidate)) return candidate;
  }
}

export function recommendedPort(ports: number[]) {
  const available = [...new Set(ports.filter((port) => port > 0))].sort((a, b) => a - b);
  if (available.length === 1) return available[0];
  return commonWebPorts.find((port) => available.includes(port)) || 0;
}

export function recommendedScheme(port: number) {
  return port === 443 || port === 8443 ? "https" : "http";
}

export function routeNameIssue(name: string, routes: Route[], editingId?: number) {
  if (!name) return "Enter a local hostname.";
  if (name.length > 63) return "Use 63 characters or fewer.";
  if (!/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(name)) return "Use lowercase letters, numbers, and single hyphens.";
  const conflict = routes.find((route) => route.name === name && route.id !== editingId);
  return conflict ? `Already used by route #${conflict.id}.` : "";
}

export function defaultReadiness(route: Route): RouteReadiness {
  return {
    routeId: route.id,
    revision: route.revision,
    state: route.enabled ? "reconciling" : "disabled",
    ready: false,
    message: route.enabled ? "Checking whether Traefik has activated this route." : "Route is disabled and is not published to Traefik.",
    checkedAt: new Date().toISOString(),
  };
}

export function writableRoute(route: Route, enabled = route.enabled) {
  return { revision: route.revision, name: route.name, selector: route.selector, port: route.port, scheme: route.scheme, enabled };
}
