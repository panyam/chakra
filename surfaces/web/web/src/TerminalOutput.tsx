import { createEffect, onCleanup, onMount } from "solid-js";
import type { Terminal } from "@xterm/xterm";

// TerminalOutput draws a tty process's output in xterm.js, at the size of the
// process's terminal, so colours show and a line a progress bar redraws
// stays one line (#218). It is read-only. xterm.js loads only when a tty
// process is on screen, and its stylesheet with it.
//
// The store keeps the tail of the output and a running count of everything
// received. Each update writes only what arrived since the last write, in
// order, so a reload that replays a process's events draws the same screen.

let cssLoaded = false;
function loadCSS(): void {
  if (cssLoaded) return;
  cssLoaded = true;
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = "/static/xterm.css";
  document.head.appendChild(link);
}

export function TerminalOutput(props: { raw: string; rawTotal: number; cols: number; rows: number }) {
  let el: HTMLDivElement | undefined;
  let term: Terminal | undefined;
  let written = 0;

  const flush = () => {
    if (!term) return;
    const delta = props.rawTotal - written;
    if (delta <= 0) return;
    term.write(delta >= props.raw.length ? props.raw : props.raw.slice(props.raw.length - delta));
    written = props.rawTotal;
  };

  onMount(async () => {
    loadCSS();
    const { Terminal } = await import("@xterm/xterm");
    if (!el) return;
    term = new Terminal({
      cols: props.cols || 120,
      rows: props.rows || 30,
      disableStdin: true,
      cursorBlink: false,
      scrollback: 2000,
      fontSize: 12,
    });
    term.open(el);
    flush();
  });

  createEffect(() => {
    props.rawTotal;
    flush();
  });

  onCleanup(() => term?.dispose());

  return <div class="tool-term" ref={el} />;
}
