# stepshell

An authenticated web shell for Kubernetes pods, aware of Argo Workflows'
**debug pause**. Deep-link straight from the Argo Workflows or Argo CD UI into a
running step pod, or re-run a workflow with a breakpoint so a too-fast step
holds still while you look inside.

Argo Workflows has no terminal in its UI and no extension point to add one
([#6945](https://github.com/argoproj/argo-workflows/issues/6945) has been open
since 2021). The debug-pause backend exists
([#6841](https://github.com/argoproj/argo-workflows/issues/6841)) but never got a
UI. Generic Kubernetes web terminals either ship without authentication or are
whole platforms. stepshell is the small, authenticated, Argo-aware piece that
was missing.

> **Pausing is optional.** A live pod is always shell-able without pausing —
> open a shell, `tail -f` a log, poke at the filesystem. Pausing is only for the
> case where the step finishes before you can catch it.

## What it does

- **Web terminal** into any pod/container, deep-linkable:
  `/shell/{namespace}/{pod}?container=main`. Optionally run a fixed command:
  `?cmd=tail%20-f%20/var/log/app.log`.
- **Per-user identity.** OIDC login, then Kubernetes **impersonation** — the
  server holds no `pods/exec` rights of its own; your cluster's RBAC decides,
  and the API-server audit log sees the real person.
- **Shell-less images.** One click attaches an ephemeral debug container
  (`nicolaka/netshoot`) sharing the target's PID namespace and volume mounts, so
  you get a shell, the target's processes (`/proc/1/root`), and its filesystem
  even on distroless images.
- **Argo aware.** Browse workflows, jump to a step's pod, **re-run with a
  breakpoint** (injects `ARGO_DEBUG_PAUSE_*`), and **release** a paused step
  from the UI. Pod names are computed for GC'd pods so the mapping still shows.
- **Standalone.** Single Go binary with the UI embedded, plus a Helm chart.

## Architecture

```
browser ──HTTPS──► stepshell ──impersonated REST/WebSocket──► kube-apiserver
 xterm.js           OIDC + AES-GCM cookie                      RBAC decides
                    exec bridge (client-go remotecommand)      audit sees the user
```

No database. The session is an encrypted cookie, so replicas only share the
session key. See [`docs/spec.md`](docs/spec.md) for the full design, threat
model and HTTP API.

## Quick start (local, no auth)

```bash
make ui           # build the embedded UI once
make run          # serves on http://localhost:8080 as $USER@local against your kube-context
```

`--auth.mode=none` is refused on any non-localhost URL unless you also pass
`--auth.allow-insecure-none`. Don't.

## Deploy

```bash
helm install stepshell ./charts/stepshell \
  --set baseURL=https://stepshell.example.com \
  --set oidc.issuer=https://dex.example.com \
  --set oidc.clientID=stepshell \
  --set oidc.clientSecret=<secret> \
  --set argo.uiURL=https://argo-workflows.example.com \
  --set ingress.enabled=true \
  --set-json 'rbac.allowedGroups=["your-oidc-group"]'
```

The server ServiceAccount gets only `impersonate` on users/groups. Grant your
users the namespaced access they need yourself, or let the chart do it:

```yaml
userAccess:
  enabled: true
  namespaces: [default, csa]
  subjects:
    - kind: Group
      name: your-oidc-group
```

### GitHub login via the bundled Dex

GitHub is not an OIDC provider, so stepshell can bring its own
[Dex](https://dexidp.io) to bridge it — self-contained, no dependency on any
other service. The `groups` claim arrives as `org:team` (e.g.
`strikesecurity:cloud`), which you pin in `rbac.allowedGroups` and can reuse in
your cluster RBAC.

1. Create a **GitHub OAuth app** (Settings → Developer settings → OAuth Apps)
   with callback URL `https://stepshell.example.com/dex/callback`.

2. Enable the bundled Dex. stepshell's `oidc.*` fields are wired to it
   automatically — you only pass the GitHub app credentials:

   ```bash
   helm install stepshell ./charts/stepshell \
     --set baseURL=https://stepshell.example.com \
     --set dex.enabled=true \
     --set dex.publicURL=https://stepshell.example.com/dex \
     --set dex.github.clientID=<oauth app id> \
     --set dex.github.clientSecret=<oauth app secret> \
     --set-json 'dex.github.orgs=[{"name":"your-org","teams":["cloud","admins"]}]' \
     --set ingress.enabled=true \
     --set-json 'rbac.allowedGroups=["your-org:cloud","your-org:admins"]'
   ```

   Dex is served under `/dex` on the same host and ingress; the chart routes it
   automatically. The stepshell↔Dex client secret is generated and kept stable
   across upgrades.

Prefer an existing IdP (Okta, Entra, Google, or a Dex you already run)? Leave
`dex.enabled=false` and set `oidc.issuer` / `oidc.clientID` / `oidc.clientSecret`
directly. The issuer must match the provider's advertised `issuer` exactly.

**Behind an AWS ALB**, raise the idle timeout so long-lived exec WebSockets
don't drop at 60s:

```yaml
ingress:
  annotations:
    alb.ingress.kubernetes.io/load-balancer-attributes: idle_timeout.timeout_seconds=3600
```

## Wire it into the Argo UIs

Argo Workflows (`controller.links`):

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

Argo CD (`argocd-cm`):

```yaml
resource.links: |
  - url: https://stepshell.example.com/shell/{{.resource.metadata.namespace}}/{{.resource.metadata.name}}
    title: Shell
    icon.class: fa-terminal
    if: resource.kind == "Pod"
```

## Debug pause, end to end

1. On a workflow page, open **Re-run with breakpoint**, choose *pause after
   step* (and optionally which templates), submit. stepshell clones the
   workflow with `ARGO_DEBUG_PAUSE_AFTER=true` injected and `podGC` set to
   `OnWorkflowCompletion` so the paused pod survives.
2. When the step reaches the breakpoint the pod stays **Running**. Open its
   **Shell**, inspect processes/filesystem/volumes.
3. Hit **Release pause** (or `touch /var/run/argo/ctr/main/after` yourself) and
   the step continues. Outputs are saved after release.

## Security

- Impersonation means a server compromise lets an attacker impersonate any user
  the `impersonate` rule allows — pin `rbac.allowedGroups`, run the distroless
  read-only image, add a NetworkPolicy.
- Inside a pod you have whatever the pod's ServiceAccount allows, exactly like
  `kubectl exec`. Set `automountServiceAccountToken: false` on pods that don't
  need the API.
- Sessions are AES-256-GCM cookies (`HttpOnly; Secure; SameSite=Lax`), bounded
  by `session.ttl`. Mutating requests require the `X-Stepshell` header; the
  WebSocket handshake checks `Origin`.

## Development

```bash
make test     # go test + tsc --noEmit
make lint     # go vet + gofmt -l
cd web && npm run watch   # rebuild UI on change
```

## License

Apache-2.0.
