export type ActiveTab = "routes" | "containers";
export type Theme = "light" | "forest";

export type RouteEligibility = {
  eligible: boolean;
  code?: "system-workload" | "routing-disabled" | "no-tcp-ports";
  reason?: string;
};

export type Container = {
  id: string;
  name: string;
  image: string;
  status: string;
  systemRole?: "reverse-proxy" | "controller" | "probe";
  composeProject?: string;
  composeService?: string;
  exposedPorts: number[];
  routeEligibility: RouteEligibility;
};

export type Selector = {
  composeProject: string;
  composeService: string;
  containerId?: string;
};

export type Observation = {
  state: "ready" | "disabled" | "unresolved" | "ambiguous" | "error";
  message?: string;
  containerName?: string;
  upstreamUrl?: string;
  checkedAt?: string;
};

export type Route = {
  id: number;
  revision: number;
  name: string;
  selector: Selector;
  port: number;
  scheme: string;
  enabled: boolean;
  observed: Observation;
};

export type RouteReadiness = {
  routeId: number;
  revision: number;
  state: "reconciling" | "publishing" | "verifying" | "ready" | "disabled" | "error";
  ready: boolean;
  message: string;
  checkedAt: string;
};

export type DiagnosticStatus = "pass" | "warn" | "fail";

export type DiagnosticCheck = {
  id: string;
  layer: string;
  status: DiagnosticStatus;
  summary: string;
  detail?: string;
  suggestion?: string;
};

export type DiagnosticReport = {
  status: DiagnosticStatus;
  target: string;
  hostname: string;
  generatedAt: string;
  checks: DiagnosticCheck[];
};

export type BrowserProbe = {
  status: "idle" | "pending" | DiagnosticStatus;
  summary: string;
  detail?: string;
};

export type HealthSnapshot = {
  id: number;
  routeId: number;
  status: DiagnosticStatus;
  recordedAt: string;
  report: DiagnosticReport;
};

export type Inventory = {
  containers: Container[];
  routes: Route[];
  baseDomain: string;
  reconcileIntervalMs: number;
};

export type WritableRoute = {
  revision?: number;
  name: string;
  selector: Selector;
  port: number;
  scheme: string;
  enabled: boolean;
};
