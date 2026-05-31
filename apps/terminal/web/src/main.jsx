import React, { useEffect, useRef } from 'react';
import { createRoot } from 'react-dom/client';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import './styles.css';

function App() {
  const hostRef = useRef(null);
  const termRef = useRef(null);
  const fitRef = useRef(null);
  const socketRef = useRef(null);

  useEffect(() => {
    let disposed = false;
    let resizeTimer = null;
    const term = new Terminal({
      cursorBlink: true,
      cursorStyle: 'block',
      fontFamily: '"Berkeley Mono", "SFMono-Regular", Consolas, "Liberation Mono", monospace',
      fontSize: 13,
      lineHeight: 1.08,
      allowProposedApi: false,
      scrollback: 5000,
      convertEol: false,
      theme: {
        background: '#020303',
        foreground: '#d6d6d6',
        cursor: '#f2f2f2',
        cursorAccent: '#020303',
        selectionBackground: '#c7cbc8',
        selectionForeground: '#050606',
        black: '#050606',
        red: '#d86b6b',
        green: '#9da39f',
        yellow: '#b7bbb6',
        blue: '#8a8a8a',
        magenta: '#9da39f',
        cyan: '#c7cbc8',
        white: '#d6d6d6',
        brightBlack: '#696969',
        brightRed: '#f09090',
        brightGreen: '#c0c6c1',
        brightYellow: '#d6d6d6',
        brightBlue: '#b7bbb6',
        brightMagenta: '#c7cbc8',
        brightCyan: '#e2e5e2',
        brightWhite: '#f2f2f2',
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostRef.current);
    fit.fit();
    term.focus();
    termRef.current = term;
    fitRef.current = fit;

    const sendResize = () => {
      const ws = socketRef.current;
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
    };

    const fitAndResize = () => {
      if (disposed) return;
      fit.fit();
      sendResize();
    };

    const onResize = () => {
      window.clearTimeout(resizeTimer);
      resizeTimer = window.setTimeout(fitAndResize, 80);
    };
    window.addEventListener('resize', onResize);

    const dataDisposable = term.onData((data) => {
      const ws = socketRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'input', data }));
      }
    });

    async function connect() {
      try {
        const res = await fetch('/api/terminal');
        const meta = await res.json();
        if (!res.ok) {
          throw new Error(meta.error || 'Terminal is not available');
        }
        if (disposed) return;
        const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const path = meta.path || '/api/terminal/ws';
        const ws = new WebSocket(`${scheme}//${window.location.host}${path}`);
        socketRef.current = ws;
        ws.addEventListener('open', () => {
          term.clear();
          term.focus();
          fitAndResize();
        });
        ws.addEventListener('message', (event) => {
          let msg;
          try {
            msg = JSON.parse(event.data);
          } catch {
            return;
          }
          if (msg.type === 'output') {
            term.write(msg.data || '');
          } else if (msg.type === 'exit') {
            term.write(`\r\n[process exited ${msg.code ?? 0}]\r\n`);
          } else if (msg.type === 'error') {
            term.write(`\r\n[${msg.data || 'terminal error'}]\r\n`);
          }
        });
        ws.addEventListener('error', () => term.write('\r\n[terminal connection error]\r\n'));
      } catch (err) {
        if (!disposed) {
          term.write(`[${err.message}]\r\n`);
        }
      }
    }

    connect();
    return () => {
      disposed = true;
      window.clearTimeout(resizeTimer);
      window.removeEventListener('resize', onResize);
      dataDisposable.dispose();
      socketRef.current?.close();
      socketRef.current = null;
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, []);

  return (
    <main className="terminal-shell">
      <section className="terminal-frame" onMouseDown={() => termRef.current?.focus()}>
        <div ref={hostRef} className="terminal-host" />
      </section>
    </main>
  );
}

createRoot(document.getElementById('root')).render(<App />);
