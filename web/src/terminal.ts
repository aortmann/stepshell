// Terminal wires an xterm.js instance to the /ws/exec WebSocket: binary frames
// both ways for I/O, JSON text frames for resize and lifecycle.

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";

export interface ExecTarget {
  namespace: string;
  pod: string;
  container: string;
  cmd?: string;
}

export interface TerminalHandle {
  dispose(): void;
}

export function openTerminal(el: HTMLElement, target: ExecTarget, onStatus: (s: string) => void): TerminalHandle {
  const term = new Terminal({
    cursorBlink: true,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
    fontSize: 13,
    theme: { background: "#0b0e14", foreground: "#c5c8c6" },
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new WebLinksAddon());
  term.open(el);
  fit.fit();

  const params = new URLSearchParams({
    ns: target.namespace,
    pod: target.pod,
    container: target.container,
  });
  if (target.cmd) params.set("cmd", target.cmd);
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(`${proto}//${location.host}/ws/exec?${params.toString()}`);
  ws.binaryType = "arraybuffer";

  const enc = new TextEncoder();
  const dec = new TextDecoder();

  const sendResize = () => {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
    }
  };

  ws.onopen = () => onStatus("connected");
  ws.onmessage = (ev) => {
    if (typeof ev.data === "string") {
      handleControl(ev.data);
    } else {
      term.write(new Uint8Array(ev.data));
    }
  };
  ws.onclose = (ev) => {
    if (ev.code === 4401) onStatus("session expired");
    else if (ev.code === 4403) onStatus("forbidden");
    else onStatus("disconnected");
    term.writeln("\r\n\x1b[90m[stepshell] session closed\x1b[0m");
  };
  ws.onerror = () => onStatus("error");

  function handleControl(raw: string) {
    try {
      const msg = JSON.parse(raw);
      if (msg.type === "ready") {
        sendResize();
        term.focus();
      } else if (msg.type === "exit") {
        const suffix = msg.error ? ` (${msg.error})` : "";
        term.writeln(`\r\n\x1b[90m[stepshell] process exited: ${msg.code}${suffix}\x1b[0m`);
      }
    } catch {
      /* ignore malformed control frame */
    }
  }

  term.onData((data) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(data));
  });

  const onWindowResize = () => {
    fit.fit();
    sendResize();
  };
  window.addEventListener("resize", onWindowResize);

  return {
    dispose() {
      window.removeEventListener("resize", onWindowResize);
      try {
        ws.close(1000);
      } catch {
        /* noop */
      }
      term.dispose();
      void dec;
    },
  };
}
