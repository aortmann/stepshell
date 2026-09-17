import { api, Me, WorkflowDetail } from "../api";
import { h, clear, phaseBadge, toast } from "../util";
import { navigate } from "../main";

export async function renderWorkflow(app: HTMLElement, ns: string, name: string, me: Me) {
  clear(app);
  const head = h("div", { class: "wf-head" });
  const nodesBox = h("div", { class: "nodes" });
  const panel = h("div", { class: "debug-panel" });
  app.append(
    h("a", { href: "/", "data-nav": "1", class: "back" }, "← namespaces"),
    head,
    panel,
    nodesBox,
  );

  let timer: number | undefined;

  const load = async () => {
    let wf: WorkflowDetail;
    try {
      wf = await api.workflow(ns, name);
    } catch (e) {
      clear(nodesBox);
      nodesBox.append(h("div", { class: "error-box" }, (e as Error).message));
      return;
    }
    renderHead(head, wf, me);
    renderNodes(nodesBox, ns, wf);
    renderPanel(panel, ns, name, wf);

    // Auto-refresh while running.
    if (wf.phase === "Running" || wf.phase === "Pending") {
      timer = window.setTimeout(load, 4000);
    }
  };

  window.addEventListener("popstate", () => timer && clearTimeout(timer), { once: true });
  load();
}

function renderHead(el: HTMLElement, wf: WorkflowDetail, me: Me) {
  clear(el);
  el.append(
    h("h1", { class: "mono" }, wf.name),
    phaseBadge(wf.phase),
  );
  if (me.argoUIURL) {
    el.append(h("a", { class: "linkbtn", href: `${me.argoUIURL}/workflows/${wf.namespace}/${wf.name}`, target: "_blank" }, "open in Argo ↗"));
  }
}

function renderNodes(el: HTMLElement, ns: string, wf: WorkflowDetail) {
  clear(el);
  const pods = wf.nodes.filter((n) => n.type === "Pod");
  if (!pods.length) {
    el.append(h("div", { class: "empty" }, "No pod steps yet."));
    return;
  }
  const table = h("table", { class: "grid" });
  const hr = h("tr");
  for (const c of ["Step", "Template", "Phase", "Pod", ""]) hr.append(h("th", {}, c));
  table.append(hr);

  for (const n of pods) {
    const actions = h("td", { class: "right" });
    if (n.podExists) {
      actions.append(
        h("a", {
          href: `/shell/${ns}/${n.podName}?container=main`,
          "data-nav": "1",
          class: "btn small",
        }, "Shell"),
      );
    } else {
      actions.append(h("span", { class: "muted small" }, "pod gone"));
    }
    table.append(h("tr", {},
      h("td", {}, n.displayName || n.name),
      h("td", { class: "muted mono small" }, n.templateName || ""),
      h("td", {}, phaseBadge(n.phase)),
      h("td", { class: "mono small muted" }, n.podName || ""),
      actions,
    ));
  }
  el.append(table);
}

function renderPanel(el: HTMLElement, ns: string, name: string, wf: WorkflowDetail) {
  clear(el);
  const templates = Array.from(new Set(wf.nodes.filter((n) => n.templateName).map((n) => n.templateName!))).sort();

  const details = h("details", { class: "breakpoint" });
  details.append(h("summary", {}, "Re-run with breakpoint"));

  const stageSel = h("select", {}) as HTMLSelectElement;
  for (const [v, label] of [["after", "pause after step"], ["before", "pause before step"], ["both", "pause before and after"]]) {
    stageSel.append(h("option", { value: v }, label));
  }

  const tmplWrap = h("div", { class: "tmpl-check" });
  const boxes: HTMLInputElement[] = [];
  for (const t of templates) {
    const cb = h("input", { type: "checkbox", value: t }) as HTMLInputElement;
    boxes.push(cb);
    tmplWrap.append(h("label", { class: "chk" }, cb, t));
  }

  const submit = h("button", { class: "btn" }, "Submit debug re-run") as HTMLButtonElement;
  submit.onclick = async () => {
    submit.disabled = true;
    const selected = boxes.filter((b) => b.checked).map((b) => b.value);
    try {
      const res = await api.workflowDebug(ns, name, stageSel.value, selected);
      (res.warnings || []).forEach((warn) => toast(warn, "info"));
      toast(`Created ${res.name}`, "info");
      navigate(`/wf/${ns}/${res.name}`);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      submit.disabled = false;
    }
  };

  details.append(
    h("p", { class: "hint" },
      "Re-runs this workflow with ARGO_DEBUG_PAUSE injected so the selected steps hold at the breakpoint. " +
      "Leave templates unchecked to pause every step. Release from the pod's Shell page.",
    ),
    h("div", { class: "row" }, h("label", {}, "When"), stageSel),
    templates.length ? h("div", { class: "row col" }, h("label", {}, "Templates (optional)"), tmplWrap) : h("span"),
    submit,
  );
  el.append(details);
}
