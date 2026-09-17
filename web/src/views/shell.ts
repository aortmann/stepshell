import { api, Me, PodDetail } from "../api";
import { h, clear, phaseBadge, toast } from "../util";
import { openTerminal, TerminalHandle } from "../terminal";

export async function renderShell(app: HTMLElement, ns: string, pod: string, _me: Me) {
  clear(app);
  const qs = new URLSearchParams(location.search);
  let container = qs.get("container") || "";
  const cmd = qs.get("cmd") || undefined;

  const sidebar = h("div", { class: "shell-side" });
  const termWrap = h("div", { class: "term-wrap" });
  const status = h("span", { class: "term-status" }, "…");

  app.append(
    h("a", { href: "/", "data-nav": "1", class: "back" }, "← namespaces"),
    h("div", { class: "shell-layout" }, sidebar, h("div", { class: "term-col" }, h("div", { class: "term-bar" }, status), termWrap)),
  );

  let detail: PodDetail;
  try {
    detail = await api.pod(ns, pod);
  } catch (e) {
    app.append(h("div", { class: "error-box" }, (e as Error).message));
    return;
  }
  if (!container) container = detail.containers[0]?.name || "main";

  let handle: TerminalHandle | undefined;
  const connect = (c: string, command?: string) => {
    handle?.dispose();
    clear(termWrap);
    status.textContent = "connecting…";
    handle = openTerminal(termWrap, { namespace: ns, pod, container: c, cmd: command }, (s) => {
      status.textContent = s;
    });
  };

  renderSidebar(sidebar, ns, pod, detail, container, {
    onSelect: (c) => {
      container = c;
      const url = new URL(location.href);
      url.searchParams.set("container", c);
      history.replaceState({}, "", url.pathname + url.search);
      connect(c);
    },
    onReconnect: () => connect(container, cmd),
  });

  window.addEventListener("popstate", () => handle?.dispose(), { once: true });
  connect(container, cmd);
}

interface SidebarCbs {
  onSelect: (c: string) => void;
  onReconnect: () => void;
}

function renderSidebar(el: HTMLElement, ns: string, pod: string, detail: PodDetail, current: string, cbs: SidebarCbs) {
  clear(el);
  el.append(
    h("div", { class: "pod-title mono" }, pod),
    h("div", { class: "pod-meta" }, phaseBadge(detail.phase), h("span", { class: "muted small" }, detail.node)),
  );

  if (detail.workflowName) {
    el.append(h("a", { class: "wf-link", href: `/wf/${ns}/${detail.workflowName}`, "data-nav": "1" }, `↖ workflow ${detail.workflowName}`));
  }

  // Container list.
  el.append(h("div", { class: "side-h" }, "Containers"));
  const list = h("div", { class: "ctr-list" });
  const allContainers = [
    ...detail.containers.map((c) => ({ ...c, ephemeral: false })),
    ...(detail.ephemeralContainers || []).map((c) => ({ ...c, ephemeral: true })),
  ];
  for (const c of allContainers) {
    const item = h("button", {
      class: `ctr ${c.name === current ? "active" : ""}`,
      onclick: () => cbs.onSelect(c.name),
    },
      h("span", { class: "ctr-name mono" }, c.name),
      c.ephemeral ? h("span", { class: "tag" }, "debug") : h("span", {}),
      c.running ? h("span", { class: "dot ok" }) : h("span", { class: "dot off" }),
    );
    list.append(item);
  }
  el.append(list);

  // Actions.
  el.append(h("div", { class: "side-h" }, "Actions"));

  // Attach debug container (for shell-less images / filesystem view).
  const dbgBtn = h("button", { class: "btn wide" }, "Attach debug container") as HTMLButtonElement;
  dbgBtn.onclick = async () => {
    dbgBtn.disabled = true;
    dbgBtn.textContent = "attaching…";
    try {
      const res = await api.attachDebug(ns, pod, current);
      toast(`Debug container ${res.container} attached`, "info");
      cbs.onSelect(res.container);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      dbgBtn.disabled = false;
      dbgBtn.textContent = "Attach debug container";
    }
  };
  el.append(dbgBtn, h("p", { class: "hint" }, "For distroless images, or to see the target's processes and filesystem (shared PID namespace)."));

  // Release pause — only meaningful for Argo pods.
  if (detail.hasVarRunArgo) {
    el.append(h("div", { class: "side-h" }, "Debug pause"));
    const stageSel = h("select", {}) as HTMLSelectElement;
    stageSel.append(h("option", { value: "after" }, "after"), h("option", { value: "before" }, "before"));
    const relBtn = h("button", { class: "btn wide" }, "Release pause") as HTMLButtonElement;
    relBtn.onclick = async () => {
      relBtn.disabled = true;
      try {
        const res = await api.release(ns, pod, current, stageSel.value);
        toast(`Released (${res.via})`, "info");
      } catch (e) {
        toast((e as Error).message, "error");
      } finally {
        relBtn.disabled = false;
      }
    };
    el.append(h("div", { class: "row" }, h("label", {}, "Marker"), stageSel), relBtn,
      h("p", { class: "hint" }, "Creates /var/run/argo/ctr/<container>/<marker> to let a paused step continue."));
  }

  el.append(h("div", { class: "side-h" }, ""), h("button", { class: "linkbtn", onclick: cbs.onReconnect }, "↻ reconnect"));
}
