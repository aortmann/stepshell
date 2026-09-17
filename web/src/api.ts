// Typed client for the stepshell JSON API. Mutating calls carry the X-Stepshell
// header (the CSRF defence); a 401 sends the browser to login.

export interface Me {
  user: string;
  groups: string[];
  email: string;
  argoEnabled: boolean;
  argoUIURL: string;
}

export interface PodSummary {
  name: string;
  namespace: string;
  phase: string;
  node: string;
  containers: string[];
  created: string;
}

export interface ContainerInfo {
  name: string;
  image: string;
  running: boolean;
  ready: boolean;
}

export interface PodDetail {
  name: string;
  namespace: string;
  phase: string;
  node: string;
  containers: ContainerInfo[];
  ephemeralContainers?: ContainerInfo[];
  hasVarRunArgo: boolean;
  workflowName?: string;
  nodeId?: string;
}

export interface WorkflowSummary {
  name: string;
  namespace: string;
  phase: string;
  startedAt?: string;
  finishedAt?: string;
  message?: string;
}

export interface WorkflowNode {
  id: string;
  name: string;
  displayName: string;
  type: string;
  templateName?: string;
  phase: string;
  podName?: string;
  podExists: boolean;
  paused: boolean;
  children?: string[];
}

export interface WorkflowDetail {
  name: string;
  namespace: string;
  phase: string;
  entrypoint?: string;
  nodes: WorkflowNode[];
}

class ApiError extends Error {
  code: number;
  constructor(code: number, message: string) {
    super(message);
    this.code = code;
  }
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  const opts: RequestInit = { method, headers, credentials: "same-origin" };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    headers["X-Stepshell"] = "1";
    opts.body = JSON.stringify(body);
  } else if (method !== "GET") {
    headers["X-Stepshell"] = "1";
  }
  const res = await fetch(path, opts);
  if (res.status === 401) {
    window.location.href = "/auth/login?rd=" + encodeURIComponent(location.pathname + location.search);
    throw new ApiError(401, "redirecting to login");
  }
  const text = await res.text();
  const data = text ? JSON.parse(text) : {};
  if (!res.ok) {
    throw new ApiError(res.status, data.error || res.statusText);
  }
  return data as T;
}

export const api = {
  me: () => req<Me>("GET", "/api/v1/me"),
  namespaces: () => req<{ namespaces: string[]; fallback?: boolean }>("GET", "/api/v1/namespaces"),
  pods: (ns: string) => req<{ pods: PodSummary[] }>("GET", `/api/v1/namespaces/${ns}/pods`),
  pod: (ns: string, pod: string) => req<PodDetail>("GET", `/api/v1/namespaces/${ns}/pods/${pod}`),
  attachDebug: (ns: string, pod: string, target: string, image?: string) =>
    req<{ container: string }>("POST", `/api/v1/namespaces/${ns}/pods/${pod}/debug`, { target, image }),
  release: (ns: string, pod: string, container: string, stage: string) =>
    req<{ released: boolean; via: string }>("POST", `/api/v1/namespaces/${ns}/pods/${pod}/release`, { container, stage }),
  workflows: (ns: string) => req<{ workflows: WorkflowSummary[] }>("GET", `/api/v1/namespaces/${ns}/workflows`),
  workflow: (ns: string, name: string) => req<WorkflowDetail>("GET", `/api/v1/namespaces/${ns}/workflows/${name}`),
  workflowDebug: (ns: string, name: string, stage: string, templates: string[]) =>
    req<{ name: string; warnings?: string[] }>("POST", `/api/v1/namespaces/${ns}/workflows/${name}/debug`, { stage, templates }),
};

export { ApiError };
