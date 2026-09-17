import { api, Me } from "../api";
import { h, clear, phaseBadge, relTime, toast } from "../util";

const LAST_NS = "stepshell.lastNamespace";

export async function renderHome(app: HTMLElement, me: Me) {
  clear(app);
  const nsSelect = h("select", { class: "ns-select" }) as HTMLSelectElement;
  const tabsBar = h("div", { class: "tabs" });
  const content = h("div", { class: "content" });

  app.append(
    h("div", { class: "toolbar" }, h("label", {}, "Namespace"), nsSelect),
    tabsBar,
    content,
  );

  let namespaces: string[] = [];
  try {
    const res = await api.namespaces();
    namespaces = res.namespaces;
    if (res.fallback) toast("Showing configured namespaces (you cannot list all).", "info");
  } catch (e) {
    toast(`Failed to load namespaces: ${(e as Error).message}`, "error");
    return;
  }

  for (const ns of namespaces) nsSelect.append(h("option", { value: ns }, ns));
  const remembered = localStorage.getItem(LAST_NS);
  if (remembered && namespaces.includes(remembered)) nsSelect.value = remembered;

  let tab: "workflows" | "pods" = me.argoEnabled ? "workflows" : "pods";

  const renderTabs = () => {
    clear(tabsBar);
    if (me.argoEnabled) {
      tabsBar.append(tabButton("Workflows", tab === "workflows", () => { tab = "workflows"; refresh(); }));
    }
    tabsBar.append(tabButton("Pods", tab === "pods", () => { tab = "pods"; refresh(); }));
  };

  const refresh = async () => {
    const ns = nsSelect.value;
    localStorage.setItem(LAST_NS, ns);
    renderTabs();
    clear(content);
    content.append(h("div", { class: "loading" }, "Loading…"));
    try {
      if (tab === "workflows") await renderWorkflowsTab(content, ns);
      else await renderPodsTab(content, ns);
    } catch (e) {
      clear(content);
      content.append(h("div", { class: "error-box" }, (e as Error).message));
    }
  };

  nsSelect.onchange = refresh;
  if (namespaces.length) refresh();
  else content.append(h("div", { class: "empty" }, "No namespaces available."));
}

function tabButton(label: string, active: boolean, onclick: () => void): HTMLElement {
  return h("button", { class: `tab ${active ? "active" : ""}`, onclick }, label);
}

async function renderWorkflowsTab(content: HTMLElement, ns: string) {
  const { workflows } = await api.workflows(ns);
  clear(content);
  if (!workflows.length) {
    content.append(h("div", { class: "empty" }, "No workflows in this namespace."));
    return;
  }
  const table = h("table", { class: "grid" });
  table.append(row("th", ["Workflow", "Phase", "Started", "Message"]));
  for (const wf of workflows) {
    const link = h("a", { href: `/wf/${ns}/${wf.name}`, "data-nav": "1", class: "mono" }, wf.name);
    const tr = h("tr", {},
      h("td", {}, link),
      h("td", {}, phaseBadge(wf.phase)),
      h("td", { class: "muted" }, relTime(wf.startedAt)),
      h("td", { class: "muted ellipsis" }, wf.message || ""),
    );
    table.append(tr);
  }
  content.append(table);
}

async function renderPodsTab(content: HTMLElement, ns: string) {
  const { pods } = await api.pods(ns);
  clear(content);
  if (!pods.length) {
    content.append(h("div", { class: "empty" }, "No pods in this namespace."));
    return;
  }
  const table = h("table", { class: "grid" });
  table.append(row("th", ["Pod", "Phase", "Containers", "Age", ""]));
  for (const p of pods) {
    const shell = h("a", { href: `/shell/${ns}/${p.name}?container=${p.containers[0] || ""}`, "data-nav": "1", class: "btn small" }, "Shell");
    table.append(h("tr", {},
      h("td", { class: "mono" }, p.name),
      h("td", {}, phaseBadge(p.phase)),
      h("td", { class: "muted" }, p.containers.join(", ")),
      h("td", { class: "muted" }, relTime(p.created)),
      h("td", { class: "right" }, shell),
    ));
  }
  content.append(table);
}

function row(cell: "th" | "td", vals: string[]): HTMLElement {
  const tr = h("tr");
  for (const v of vals) tr.append(h(cell, {}, v));
  return tr;
}
