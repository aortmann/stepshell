# stepshell — design specification

Status: v1 design, implemented in this repository.

## 1. Problem

Argo Workflows has no way to open a shell into a step pod from its UI, and no
UI-extension mechanism through which one could be added
([argoproj/argo-workflows#6945](https://github.com/argoproj/argo-workflows/issues/6945),
open since 2021; [#7949](https://github.com/argoproj/argo-workflows/issues/7949):
"use `kubectl exec`"). Generic Kubernetes web terminals exist, but they either
ship without authentication (k8shell, kube-webshell) or are full platforms
(Wayne, Rancher). None of them understands Argo's *debug pause* mechanism
([#6841](https://github.com/argoproj/argo-workflows/issues/6841)), which was
implemented backend-only: two environment variables and a marker file.

What operators actually want:

- open a shell into a **running** step pod straight from the Argo UI — tail a
  log, look at files on a volume, see what processes are doing;
- get in even when the step image has no shell;
- when a step is too short-lived to catch, re-run it with a **breakpoint** and
  release it once done inspecting;
- every session attributable to a person, authorized by the cluster's own RBAC.

## 2. Goals and non-goals

Goals

- **G1** Deep-linkable web terminal into any pod/container:
  `/shell/{namespace}/{pod}?container=main`.
- **G2** Per-user identity. OIDC login, then Kubernetes *impersonation*. The
  server holds no `pods/exec` rights of its own; the cluster's RBAC decides.
- **G3** Works on shell-less (distroless) images through ephemeral debug
  containers that share the target's PID namespace and volume mounts.
- **G4** Argo Workflows awareness, optional: workflow/node view, "re-run with
  breakpoint", release. **Pausing is opt-in per re-run; a live pod is always
  shell-able without pausing.**
- **G5** Standalone: single binary with embedded UI, Helm chart. Integrates with
  Argo Workflows and Argo CD through their native link configuration.
- **G6** Auditable: structured log line and Kubernetes Event per session;
  impersonated identity shows up in API-server audit logs.

Non-goals for v1

- Multi-cluster.
- Session recording / replay.
- File upload/download (use the debug container).
- Being a general Kubernetes dashboard.
- OIDC refresh tokens: the session TTL bounds session length instead.
- Server-side session store: sessions are encrypted cookies, so replicas only
  need to share the session key.

## 3. Architecture

```
 browser ──── HTTPS ────► stepshell ──── impersonated REST / WebSocket ────► kube-apiserver
   │ xterm.js                │ OIDC client (code + PKCE)                        │ RBAC decides:
   │ SPA (embedded)          │ session = AES-GCM cookie                         │ pods/exec
   └─ WS /ws/exec ───────────┤ exec bridge (client-go remotecommand)            │ pods/ephemeralcontainers
                             └─ audit: slog JSON + Kubernetes Events            │ workflows.argoproj.io
 IdP (Dex / Okta / Google / Entra …) ◄── authorization code ──┘
```

One Go binary, no database. Request lifecycle: session cookie → identity
(`user`, `groups`) → `rest.ImpersonationConfig` → per-request client → API
call. When RBAC denies, the user sees the API server's own `Forbidden` message.

## 4. Identity and authorization

### 4.1 Auth modes

- `oidc` — production. Authorization-code flow with PKCE (S256) and nonce.
- `none` — development only. A fixed identity from `--dev.user` /
  `--dev.groups`. Logs a warning at startup and refuses to start unless
  `--base-url` is a localhost URL or `--auth.allow-insecure-none` is set.

### 4.2 OIDC claims → Kubernetes identity

- Username from `--oidc.username-claim` (default `email`), groups from
  `--oidc.groups-claim` (default `groups`).
- Optional `--impersonate.user-prefix` / `--impersonate.group-prefix` so the
  impersonated subjects match whatever convention the cluster's RBAC uses
  (mirrors kube-apiserver's `--oidc-username-prefix` / `--oidc-groups-prefix`).
- Session cookie: `{user, groups, email, iat, exp}` encrypted and authenticated
  with AES-256-GCM. `HttpOnly; Secure; SameSite=Lax`. TTL default `8h`. `Lax`
  so that top-level navigations from the Argo UI carry the cookie (deep-links
  work) while cross-site POSTs do not.

### 4.3 Impersonation

Every Kubernetes call carries `Impersonate-User` and `Impersonate-Group`
headers. The server's ServiceAccount needs exactly:

- `impersonate` on `users` and `groups` (cluster-scoped by nature);
- `create` on `events` for the audit trail (optional).

It holds no `pods/exec`, `pods/ephemeralcontainers`, or Argo permissions. Trust
boundary: whoever controls the stepshell process can impersonate any user the
rule allows. Mitigations: pin allowed groups with `resourceNames` on the
`impersonate groups` rule when the set is known, NetworkPolicy, distroless
read-only image. This is the same model used by kube-oidc-proxy, Teleport and
Dex-fronted gateways.

### 4.4 What a user needs (RBAC)

Namespaced Role, bound to the user or their group:

| resource | verbs | needed for |
|---|---|---|
| `pods` | get, list, watch | pod picker, pod page |
| `pods/exec` | create | terminal, pause release |
| `pods/ephemeralcontainers` | update | debug container |
| `pods/log` | get | (optional, future) |
| `workflows.argoproj.io` | get, list, create | Argo view, breakpoint re-run |
| `workflowtemplates.argoproj.io` | get | inlining `workflowTemplateRef` |
| `clusterworkflowtemplates.argoproj.io` (ClusterRole) | get | same, cluster-scoped |

The chart renders these from `userAccess` in values.

### 4.5 CSRF and origin

- Mutating endpoints require the header `X-Stepshell: 1`. Browsers never attach
  custom headers cross-site without a CORS preflight, which is never granted.
- WebSocket handshakes must present an `Origin` equal to `--base-url`.
- `rd` (return-to after login) must be a local absolute path (`/…`, not `//…`).

## 5. Features

### 5.1 Terminal

`GET /ws/exec?ns=&pod=&container=&cmd=` upgrades to a WebSocket and runs a
TTY exec through `client-go/tools/remotecommand` (WebSocket transport first,
SPDY fallback).

- Default command: `/bin/sh -c 'export TERM=xterm-256color; command -v bash >/dev/null 2>&1 && exec bash || exec sh'`.
- `cmd` runs `sh -c <cmd>` instead — deep-links like `?cmd=tail -f /var/log/app.log`.
- Resize is forwarded; the server pings every 25s so load-balancer idle
  timeouts (ALB default 60s) don't kill quiet sessions.
- When exec fails with `executable file not found` the UI offers "Attach debug
  container".

### 5.2 Debug container (shell-less images, filesystem, processes)

`POST /api/v1/namespaces/{ns}/pods/{pod}/debug` `{target, image?}` appends an
`EphemeralContainer`:

- `name: stepshell-<rand>`, `image` (default `nicolaka/netshoot`),
  `command: ["tail","-f","/dev/null"]`, `targetContainerName: <target>`;
- `volumeMounts`: the target container's mounts (excluding projected
  ServiceAccount tokens) plus `var-run-argo` when the pod has it, so Argo's
  marker files are reachable.

Waits until the container is running (60s), returns its name. An existing
running `stepshell-*` container targeting the same container is reused.
Ephemeral containers cannot be removed; they end with the pod.

Inside the debug container: `ps` shows the target's processes (shared PID
namespace), `/proc/1/root` is the target's root filesystem, and the target's
volumes are mounted at the same paths.

### 5.3 Argo Workflows (`--argo.enabled`)

- `GET /api/v1/namespaces/{ns}/workflows` — recent workflows.
- `GET /api/v1/namespaces/{ns}/workflows/{name}` — nodes flattened: id, name,
  displayName, type, templateName, phase, timestamps, `podName`, `podExists`,
  `paused` (derived: node Running and pod annotation/labels), containers.
  Pod names come from live pods (`workflows.argoproj.io/workflow` label,
  `workflows.argoproj.io/node-id` annotation); for deleted pods the v2 name is
  computed (`<wf>-<template>-<fnv32a(nodeName)>`) and `podExists=false`.
- `POST /api/v1/namespaces/{ns}/workflows/{name}/debug`
  `{stage: "before"|"after"|"both", templates: [..]}` creates
  `<name>-dbg-<rand>`:
  - metadata stripped; labels `stepshell.io/debug-of=<name>`; annotation
    `stepshell.io/requested-by=<user>`;
  - `spec.podGC.strategy=OnWorkflowCompletion` so paused/finished pods survive
    while the debug run lives; `spec.ttlStrategy.secondsAfterCompletion` set
    from `--argo.debug-ttl`;
  - a top-level `workflowTemplateRef` is resolved and **inlined** (templates,
    entrypoint, arguments merged by name, volumes, …), because the env has to
    land on the template's container for Argo to also lift
    `activeDeadlineSeconds` (`workflowpod.go` checks template env, and
    `podSpecPatch` is applied *after* that check);
  - `ARGO_DEBUG_PAUSE_BEFORE` / `_AFTER` = `"true"` injected into `container`,
    `script` and `containerSet` entries of the selected templates (empty
    selection = all);
  - if a selected template cannot be found inline (step-level `templateRef`),
    falls back to a workflow-level `podSpecPatch` on container `main` and
    clears `spec.activeDeadlineSeconds`; the response carries a warning.
- `POST /api/v1/namespaces/{ns}/pods/{pod}/release` `{container, stage}`
  creates `/var/run/argo/ctr/<container>/<stage>`: first `sh -c touch` inside
  the container; when the image has no shell (`argoexec` itself is distroless,
  so the `wait` sidecar is no help), attaches or reuses a debug container with
  `var-run-argo` mounted and touches from there. Response says which path was
  taken.

### 5.4 UI

- `/` — namespace picker; tabs *Workflows* (when Argo is enabled) and *Pods*.
- `/wf/{ns}/{name}` — node table with phase, pod, *Shell* (live pods),
  *Release* (paused nodes); "Re-run with breakpoint" panel (templates,
  before/after). Auto-refresh while the workflow is running.
- `/shell/{ns}/{pod}?container=&cmd=` — terminal, container selector, pod
  facts, *Attach debug container*, *Release pause* (Argo pods), link back to
  the workflow.

## 6. HTTP API

All endpoints are JSON and require a session, except `/auth/*`, `/healthz`,
`/readyz` and static assets.

| method | path | notes |
|---|---|---|
| GET | `/api/v1/me` | identity, capabilities (argo on/off) |
| GET | `/api/v1/namespaces` | impersonated list; falls back to `--namespaces` |
| GET | `/api/v1/namespaces/{ns}/pods` | |
| GET | `/api/v1/namespaces/{ns}/pods/{pod}` | containers, ephemeral containers, argo labels |
| POST | `/api/v1/namespaces/{ns}/pods/{pod}/debug` | attach debug container |
| POST | `/api/v1/namespaces/{ns}/pods/{pod}/release` | release debug pause |
| GET | `/api/v1/namespaces/{ns}/workflows` | argo |
| GET | `/api/v1/namespaces/{ns}/workflows/{name}` | argo |
| POST | `/api/v1/namespaces/{ns}/workflows/{name}/debug` | argo |
| GET | `/ws/exec` | WebSocket |
| GET | `/auth/login?rd=` | |
| GET | `/auth/callback` | |
| POST | `/auth/logout` | |
| GET | `/healthz`, `/readyz` | |

Errors: `{"error": "<message>", "code": <http status>}`. Kubernetes
`StatusError`s pass through their status code and message so a `Forbidden`
reads exactly like `kubectl`.

## 7. WebSocket protocol

Client → server

- binary frame: stdin bytes
- text frame: `{"type":"resize","cols":N,"rows":N}`

Server → client

- binary frame: terminal output
- text frame: `{"type":"ready","session":"<id>"}`,
  `{"type":"exit","code":N,"error":"<message>"}`

Close codes: `1000` normal, `4401` unauthenticated, `4403` forbidden,
`4404` not found, `4500` exec error.

## 8. Configuration

Flags, each also readable from `STEPSHELL_<UPPER_SNAKE>`; env `STEPSHELL_OIDC_CLIENT_SECRET`
is the expected way to pass the secret.

| flag | default | |
|---|---|---|
| `--listen` | `:8080` | |
| `--base-url` | required | public URL, used for OIDC redirect and Origin check |
| `--auth.mode` | `oidc` | `oidc` or `none` |
| `--oidc.issuer` | | |
| `--oidc.client-id` | | |
| `--oidc.client-secret` | | env only, recommended |
| `--oidc.scopes` | `openid,profile,email,groups` | |
| `--oidc.username-claim` | `email` | |
| `--oidc.groups-claim` | `groups` | |
| `--impersonate.user-prefix` | | |
| `--impersonate.group-prefix` | | |
| `--session.key` | random | base64, 32 bytes; set it for restarts/replicas |
| `--session.ttl` | `8h` | |
| `--namespaces` | | fallback list when the user can't list namespaces |
| `--debug.image` | `nicolaka/netshoot:latest` | |
| `--exec.default-command` | see 5.1 | |
| `--argo.enabled` | `true` | |
| `--argo.ui-url` | | for "open in Argo" links |
| `--argo.debug-ttl` | `3600` | seconds, `ttlStrategy` on debug runs |
| `--kubeconfig` | | empty = in-cluster |
| `--dev.user`, `--dev.groups` | | auth mode `none` |
| `--audit.events` | `true` | Kubernetes Event per exec |
| `--log.level` | `info` | |

## 9. Deployment

Helm chart `charts/stepshell`:

- Deployment: distroless nonroot image, `readOnlyRootFilesystem`, drop all caps.
- Service, Ingress (annotations pass-through; note ALB `idle_timeout`).
- ServiceAccount, ClusterRole (`impersonate users/groups`, `create events`),
  ClusterRoleBinding.
- Secret for OIDC client secret and session key; `existingSecret` supported.
- Optional `userAccess`: per-namespace Role + RoleBinding for the subjects
  listed (section 4.4), plus the ClusterRole for `clusterworkflowtemplates`.
- Probes on `/healthz` and `/readyz`.

## 10. Integration with Argo

Argo Workflows (`controller.links` in the Helm chart, or `links:` in the
workflow-controller ConfigMap):

```yaml
- name: Shell
  scope: pod
  target: _blank
  url: https://stepshell.example.com/shell/${metadata.namespace}/${metadata.name}?container=main
- name: Debug workflow
  scope: workflow
  target: _blank
  url: https://stepshell.example.com/wf/${metadata.namespace}/${metadata.name}
```

Argo CD (`argocd-cm`, deep links):

```yaml
resource.links: |
  - url: https://stepshell.example.com/shell/{{.resource.metadata.namespace}}/{{.resource.metadata.name}}
    title: Shell
    icon.class: fa-terminal
    if: resource.kind == "Pod"
```

## 11. Audit

`slog` JSON events: `auth.login`, `auth.logout`, `exec.start`, `exec.end`
(duration, bytes in/out, exit code), `debug.attach`, `argo.debug.submit`,
`argo.release`. Common fields: `user`, `groups`, `session`, `namespace`,
`pod`, `container`, `command`, `remote_addr`.

With `--audit.events`, a Kubernetes Event (`reason=StepshellExec`) is recorded
on the pod, so `kubectl describe pod` shows who opened a shell. Because every
call is impersonated, the API server's audit log attributes `pods/exec` to the
real user.

## 12. Threat model, in short

- **Server compromise** ⇒ impersonation of any allowed user. Keep the process
  small: distroless, read-only FS, NetworkPolicy, pinned groups.
- **Cookie theft** ⇒ bounded by TTL; `Secure`, `HttpOnly`; logout clears.
- **Inside the pod** the user has whatever the pod's ServiceAccount token
  allows, exactly as with `kubectl exec`. Set `automountServiceAccountToken:
  false` on workflow pods that don't need the API.
- **Debug containers** mount the target's volumes: same exposure as exec into
  the target, by design.
- **Open redirect** prevented by validating `rd`.

## 13. Roadmap

- Session recording (asciicast) to object storage.
- File browser / download from the debug container.
- Multi-cluster via kubeconfig contexts.
- Refresh tokens.
- Argo Workflows UI plugin, if #6945 ever lands.
