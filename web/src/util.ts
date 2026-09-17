// Small DOM helpers to keep the views dependency-free (no framework).

type Attr = string | ((e: Event) => void) | boolean;

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, Attr> = {},
  ...children: (Node | string)[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class" && typeof v === "string") el.className = v;
    else if (k.startsWith("data-") && typeof v === "string") el.setAttribute(k, v);
    else if (k === "href" && typeof v === "string") el.setAttribute("href", v);
    else (el as any)[k] = v;
  }
  for (const c of children) el.append(c);
  return el;
}

export function clear(el: HTMLElement) {
  while (el.firstChild) el.removeChild(el.firstChild);
}

export function phaseBadge(phase: string): HTMLElement {
  const cls =
    phase === "Succeeded"
      ? "ok"
      : phase === "Failed" || phase === "Error"
        ? "err"
        : phase === "Running"
          ? "run"
          : "pend";
  return h("span", { class: `badge ${cls}` }, phase || "—");
}

export function relTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso).getTime();
  if (!d) return "";
  const secs = Math.round((Date.now() - d) / 1000);
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.round(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.round(secs / 3600)}h ago`;
  return `${Math.round(secs / 86400)}d ago`;
}

export function toast(msg: string, kind: "info" | "error" = "info") {
  const t = h("div", { class: `toast ${kind}` }, msg);
  document.body.append(t);
  setTimeout(() => t.classList.add("show"), 10);
  setTimeout(() => {
    t.classList.remove("show");
    setTimeout(() => t.remove(), 300);
  }, 4000);
}
