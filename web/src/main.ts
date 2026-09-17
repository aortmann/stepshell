// Entry point: a minimal history-API router dispatching to three views.

import { api, Me } from "./api";
import { h, clear } from "./util";
import { renderHome } from "./views/home";
import { renderWorkflow } from "./views/workflow";
import { renderShell } from "./views/shell";

let me: Me | null = null;

export function navigate(path: string) {
  history.pushState({}, "", path);
  route();
}

// Delegate clicks on [data-nav] anchors to the SPA router.
document.addEventListener("click", (e) => {
  const a = (e.target as HTMLElement).closest("a[data-nav]") as HTMLAnchorElement | null;
  if (a && a.href && a.origin === location.origin) {
    e.preventDefault();
    navigate(a.pathname + a.search);
  }
});
window.addEventListener("popstate", route);

function route() {
  const app = document.getElementById("app")!;
  const path = location.pathname;

  // /shell/{ns}/{pod}
  let m = path.match(/^\/shell\/([^/]+)\/([^/]+)\/?$/);
  if (m) {
    renderShell(app, decodeURIComponent(m[1]), decodeURIComponent(m[2]), me!);
    return;
  }
  // /wf/{ns}/{name}
  m = path.match(/^\/wf\/([^/]+)\/([^/]+)\/?$/);
  if (m) {
    renderWorkflow(app, decodeURIComponent(m[1]), decodeURIComponent(m[2]), me!);
    return;
  }
  // home
  renderHome(app, me!);
}

async function boot() {
  try {
    me = await api.me();
  } catch {
    return; // api.me() redirects to login on 401
  }
  renderChrome(me);
  route();
}

function renderChrome(me: Me) {
  const header = document.getElementById("header")!;
  clear(header);
  header.append(
    h("a", { class: "brand", href: "/", "data-nav": "1" }, "stepshell"),
    h("div", { class: "spacer" }),
    h("span", { class: "user" }, me.user),
    h(
      "button",
      {
        class: "linkbtn",
        onclick: async () => {
          await fetch("/auth/logout", { method: "POST", headers: { "X-Stepshell": "1" }, credentials: "same-origin" });
          location.href = "/auth/login";
        },
      },
      "logout",
    ),
  );
}

boot();
