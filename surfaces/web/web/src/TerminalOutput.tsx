import { createEffect, onCleanup, onMount } from "solid-js";
import type { Terminal } from "@xterm/xterm";
import type { FitAddon } from "@xterm/addon-fit";

// TerminalOutput draws a tty process's output in xterm.js, so colours show
// and a line a progress bar redraws stays one line (#218). xterm.js loads
// only when a tty process is on screen, and its stylesheet with it.
//
// The store keeps the tail of the output and a running count of everything
// received. Each update writes only what arrived since the last write, in
// order, so a reload that replays a process's events draws the same screen.
//
// While the process runs, the view fits its panel and asks for the process's
// terminal to be resized to match, and what the user types goes to the
// process. Once it has exited, the view is read-only.

// MIN_COLS and MIN_ROWS are the smallest fit the view takes.
const MIN_COLS = 20;
const MIN_ROWS = 3;

let cssLoaded = false;
function loadCSS(): void {
  if (cssLoaded) return;
  cssLoaded = true;
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = "/static/xterm.css";
  document.head.appendChild(link);
}

export interface TerminalProps {
  raw: string;
  rawTotal: number;
  cols: number;
  rows: number;
  running: boolean;
  // onResize asks for the process's terminal to take the view's size.
  onResize?: (cols: number, rows: number) => void;
  // onInput sends what the user typed.
  onInput?: (data: string) => void;
}

export function TerminalOutput(props: TerminalProps) {
  let el: HTMLDivElement | undefined;
  let term: Terminal | undefined;
  let fit: FitAddon | undefined;
  let observer: ResizeObserver | undefined;
  let resizeTimer: ReturnType<typeof setTimeout> | undefined;
  let written = 0;
  // ready is set once the view has its size. Output written before that would
  // be reflowed when the size changes, and a line a progress bar redrew in
  // place comes out garbled.
  let ready = false;

  const flush = () => {
    if (!term || !ready) return;
    const delta = props.rawTotal - written;
    if (delta <= 0) return;
    term.write(delta >= props.raw.length ? props.raw : props.raw.slice(props.raw.length - delta));
    written = props.rawTotal;
  };

  // refit sizes the view to the panel while the process runs, and the
  // process's terminal follows at most every 150ms while a panel is dragged.
  // An exited process keeps the size it ran at. A hidden or collapsed panel
  // proposes a size too small to use, so that waits for the next resize.
  const refit = () => {
    if (!term || !fit) return;
    if (!props.running) {
      ready = true;
      return;
    }
    const dims = fit.proposeDimensions();
    if (!dims || dims.cols < MIN_COLS || dims.rows < MIN_ROWS) return;
    ready = true;
    if (dims.cols === term.cols && dims.rows === term.rows) return;
    term.resize(dims.cols, dims.rows);
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => props.onResize?.(dims.cols, dims.rows), 150);
  };

  onMount(async () => {
    loadCSS();
    const [{ Terminal }, { FitAddon }] = await Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]);
    if (!el) return;
    term = new Terminal({
      cols: props.cols || 120,
      rows: props.rows || 30,
      disableStdin: !props.running,
      cursorBlink: false,
      scrollback: 2000,
      fontSize: 12,
    });
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    term.onData((data) => {
      if (props.running) props.onInput?.(data);
    });
    refit();
    flush();
    observer = new ResizeObserver(() => {
      refit();
      flush();
    });
    observer.observe(el);
  });

  createEffect(() => {
    props.rawTotal;
    flush();
  });

  createEffect(() => {
    const running = props.running;
    if (term) term.options.disableStdin = !running;
  });

  onCleanup(() => {
    observer?.disconnect();
    if (resizeTimer) clearTimeout(resizeTimer);
    term?.dispose();
  });

  return <div class="tool-term" ref={el} />;
}
