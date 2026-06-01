import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

const foxlabAscii = [
  ' ____    _____    __   __   __       ______  ____      ',
  '/\\  _`\\ /\\  __`\\ /\\ \\ /\\ \\ /\\ \\     /\\  _  \\/\\  _`\\    ',
  '\\ \\ \\L\\_\\ \\ \\/\\ \\\\ `\\`\\/\'/\'\\ \\ \\    \\ \\ \\L\\ \\ \\ \\L\\ \\  ',
  ' \\ \\  _\\/\\ \\ \\ \\ \\`\\/ > <   \\ \\ \\  __\\ \\  __ \\ \\  _ <\' ',
  '  \\ \\ \\/  \\ \\ \\_\\ \\  \\/\'/\\`\\ \\ \\ \\L\\ \\\\ \\ \\/\\ \\ \\ \\L\\ \\',
  '   \\ \\_\\   \\ \\_____\\ /\\_\\\\ \\_\\\\ \\____/ \\ \\_\\ \\_\\ \\____/',
  '    \\/_/    \\/_____/ \\/_/ \\/_/ \\/___/   \\/_/\\/_/\\/___/',
].join('\n');

function App() {
  const [apps, setApps] = useState([]);
  const [windows, setWindows] = useState([]);
  const [activeWindowId, setActiveWindowId] = useState('');
  const [resizingWindowId, setResizingWindowId] = useState('');
  const [appState, setAppState] = useState('stopped');
  const [message, setMessage] = useState('');
  const [launching, setLaunching] = useState(false);
  const [startOpen, setStartOpen] = useState(false);
  const [desktopListing, setDesktopListing] = useState(null);

  useEffect(() => {
    apiJSON('/api/apps')
      .then((items) => {
        const nextApps = normalizeApps(items);
        setApps(nextApps);
        if (nextApps.length === 0) {
          setAppState('missing');
          setMessage('no fox apps found');
          return;
        }
        setAppState('ready');
        setMessage('');
      })
      .catch((err) => {
        setAppState('error');
        setMessage(err.message);
      });
  }, []);

  useEffect(() => {
    loadDesktopPath();
  }, []);

  useEffect(() => {
    const events = new EventSource('/api/wm/events');
    events.addEventListener('open-window', (event) => {
      let detail;
      try {
        detail = JSON.parse(event.data);
      } catch {
        return;
      }
      const windowId = windowIdFromDetail(detail);
      setWindows((current) => upsertWindow(current, windowFromDetail(detail, current.length)));
      setActiveWindowId(windowId);
      setApps((current) => updateAppMeta(current, appMetaFromWindow(detail), 'running'));
      setAppState('running');
      setMessage('');
    });
    events.addEventListener('close-window', (event) => {
      let detail;
      try {
        detail = JSON.parse(event.data);
      } catch {
        return;
      }
      const windowId = windowIdFromDetail(detail);
      setWindows((current) => current.filter((item) => item.id !== windowId));
      setActiveWindowId((current) => (current === windowId ? '' : current));
      if (detail.appId) {
        setApps((current) => current.map((item) => (
          item.id === detail.appId ? { ...item, state: 'stopped' } : item
        )));
      }
      setAppState('stopped');
      setMessage('');
    });
    return () => events.close();
  }, []);

  useEffect(() => {
    function onMessage(event) {
      if (event.data?.type !== 'foxlab:open-file' || !event.data.path) return;
      openFile(event.data.path);
    }
    window.addEventListener('message', onMessage);
    return () => window.removeEventListener('message', onMessage);
  }, [apps, launching]);

  async function loadDesktopPath(nextPath) {
    try {
      const suffix = nextPath ? `?path=${encodeURIComponent(nextPath)}` : '';
      const data = await apiJSON(`/api/desktop${suffix}`);
      setDesktopListing(data);
    } catch (err) {
      setAppState('error');
      setMessage(err.message);
    }
  }

  async function openApp(meta, options = {}) {
    if (launching) return;
    if (!meta?.id) {
      setAppState('missing');
      setMessage('no fox apps found');
      return;
    }
    setLaunching(true);
    setStartOpen(false);
    setMessage(`starting ${meta.name}`);
    try {
      const suffix = options.wmPath ? `?path=${encodeURIComponent(options.wmPath)}` : '';
      const data = await apiJSON(`/api/apps/${encodeURIComponent(meta.id)}${suffix}`, { method: 'POST' });
      setApps((current) => updateAppMeta(current, appMetaFromStatus(data), data.state || 'starting'));
      setAppState(data.state || 'starting');
      setMessage('waiting for wm request');
    } catch (err) {
      setAppState('error');
      setMessage(err.message);
    } finally {
      setLaunching(false);
    }
  }

  async function closeWindow(windowId, event) {
    event.stopPropagation();
    const closed = windows.find((item) => item.id === windowId);
    setWindows((current) => current.filter((item) => item.id !== windowId));
    if (activeWindowId === windowId) {
      const fallback = windows.find((item) => item.id !== windowId && !item.minimized);
      setActiveWindowId(fallback?.id || '');
    }
    if (closed?.appMeta.id && apps.some((item) => item.id === closed.appMeta.id)) {
      setAppState('stopping');
      setMessage(`stopping ${closed.appMeta.name}`);
      try {
        const data = await apiJSON(`/api/apps/${encodeURIComponent(closed.appMeta.id)}`, { method: 'DELETE' });
        setApps((current) => updateAppMeta(current, appMetaFromStatus(data), data.state || 'stopped'));
        setAppState(data.state || 'stopped');
        setMessage('');
      } catch (err) {
        setAppState('error');
        setMessage(err.message);
      }
    }
  }

  function minimizeWindow(windowId, event) {
    event.stopPropagation();
    updateWindow(windowId, { minimized: true });
    if (activeWindowId === windowId) {
      const fallback = windows.find((item) => item.id !== windowId && !item.minimized);
      setActiveWindowId(fallback?.id || '');
    }
  }

  function restoreWindow(windowId) {
    updateWindow(windowId, { minimized: false });
    bringWindowToFront(windowId);
  }

  function toggleMaximizeWindow(windowId, event) {
    event.stopPropagation();
    setWindows((current) => current.map((item) => (
      item.id === windowId
        ? { ...item, minimized: false, maximized: !item.maximized, z: nextZ(current) }
        : item
    )));
    setActiveWindowId(windowId);
  }

  function openDesktopEntry(entry) {
    openFile(entry.path);
  }

  function openDesktopEntryFromKey(entry, event) {
    if (event.key !== 'Enter') return;
    event.preventDefault();
    openDesktopEntry(entry);
  }

  async function openFile(path) {
    if (launching) return;
    setLaunching(true);
    setStartOpen(false);
    setMessage('opening file');
    try {
      const data = await apiJSON('/api/files/open', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path }),
      });
      if (data?.status) {
        setApps((current) => updateAppMeta(current, appMetaFromStatus(data.status), data.status.state || 'starting'));
        setAppState(data.status.state || 'starting');
      }
      setMessage('waiting for wm request');
    } catch (err) {
      setAppState('error');
      setMessage(err.message);
    } finally {
      setLaunching(false);
    }
  }

  function startWindowDrag(event, item) {
    if (item.maximized || item.minimized) return;
    if (event.button !== 0) return;
    event.preventDefault();
    bringWindowToFront(item.id);
    event.currentTarget.setPointerCapture?.(event.pointerId);
    const origin = { ...item.rect };
    const startX = event.clientX;
    const startY = event.clientY;
    function onMove(move) {
      updateWindow(item.id, {
        rect: clampWindowRect({
          ...origin,
          x: origin.x + move.clientX - startX,
          y: origin.y + move.clientY - startY,
        }),
      });
    }
    function onUp() {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
      event.currentTarget.releasePointerCapture?.(event.pointerId);
    }
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  }

  function startWindowResize(event, item, edges) {
    if (item.maximized || item.minimized) return;
    if (event.button !== 0) return;
    event.preventDefault();
    event.stopPropagation();
    bringWindowToFront(item.id);
    event.currentTarget.setPointerCapture?.(event.pointerId);
    setResizingWindowId(item.id);
    const origin = { ...item.rect };
    const startX = event.clientX;
    const startY = event.clientY;
    function onMove(move) {
      updateWindow(item.id, {
        rect: resizeWindowRect(origin, move.clientX - startX, move.clientY - startY, edges),
      });
    }
    function onUp() {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
      event.currentTarget.releasePointerCapture?.(event.pointerId);
      setResizingWindowId('');
    }
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  }

  function updateWindow(windowId, patch) {
    setWindows((current) => current.map((item) => (
      item.id === windowId ? { ...item, ...patch } : item
    )));
  }

  function bringWindowToFront(windowId) {
    setWindows((current) => current.map((item) => (
      item.id === windowId ? { ...item, z: nextZ(current), minimized: false } : item
    )));
    setActiveWindowId(windowId);
  }

  const minimizedWindows = windows.filter((item) => item.minimized);
  const visibleWindows = windows.filter((item) => !item.minimized);
  const activeWindow = windows.find((item) => item.id === activeWindowId);
  const barStatus = message || (activeWindow ? taskbarLabel(activeWindow.appMeta) : windows.length > 0 ? `${windows.length} windows` : 'ready');
  const desktopEntries = desktopListing?.entries || [];

  return (
    <div className="desktop">
      <pre className="desktop-wordmark" aria-label="FOXLAB">{foxlabAscii}</pre>
      <div className="desktop-icons">
        {desktopEntries.map((entry, index) => (
          <button
            key={`desktop:${entry.path}`}
            className="desktop-icon desktop-file"
            style={desktopIconPosition(index)}
            onDoubleClick={() => openDesktopEntry(entry)}
            onKeyDown={(event) => openDesktopEntryFromKey(entry, event)}
            aria-label={`Open ${entry.name}`}
          >
            <FileIcon entry={entry} />
            <span className="desktop-icon-label">{entry.name}</span>
          </button>
        ))}
      </div>

      {visibleWindows.map((item) => {
        const windowTitle = `foxlab / ${item.appMeta.windowTitle}`;
        const windowStyle = item.maximized
          ? { left: 8, top: 8, width: 'calc(100vw - 16px)', height: 'calc(100vh - 44px)', zIndex: item.z }
          : { left: item.rect.x, top: item.rect.y, width: item.rect.width, height: item.rect.height, zIndex: item.z };
        return (
          <section
            key={item.id}
            className={`lab-window ${item.id === activeWindowId ? 'active' : ''} ${item.maximized ? 'maximized' : ''} ${resizingWindowId === item.id ? 'resizing' : ''}`}
            style={windowStyle}
            onPointerDown={() => bringWindowToFront(item.id)}
          >
            <header className="window-titlebar" onPointerDown={(event) => startWindowDrag(event, item)}>
              <span>{windowTitle}</span>
              <div className="window-controls">
                <button aria-label="Minimize" onPointerDown={(event) => event.stopPropagation()} onClick={(event) => minimizeWindow(item.id, event)}>_</button>
                <button aria-label={item.maximized ? 'Restore' : 'Maximize'} onPointerDown={(event) => event.stopPropagation()} onClick={(event) => toggleMaximizeWindow(item.id, event)}>[]</button>
                <button aria-label="Close" onPointerDown={(event) => event.stopPropagation()} onClick={(event) => closeWindow(item.id, event)}>x</button>
              </div>
            </header>
            <main className="iframe-stage">
              <iframe className="app-frame" title={item.appMeta.windowTitle} src={item.url} />
            </main>
            {!item.maximized && (
              <div className="window-resize-zones" aria-hidden="true">
                {resizeEdges.map((edge) => (
                  <span
                    key={edge}
                    className={`window-resize-zone resize-${edge}`}
                    onPointerDown={(event) => startWindowResize(event, item, edge)}
                  />
                ))}
              </div>
            )}
          </section>
        );
      })}

      <footer className="desktop-bar" aria-label="window bar">
        <div className="desktop-bar-brand">
          <button className={`start-button ${startOpen ? 'active' : ''}`} onClick={() => setStartOpen((current) => !current)}>foxlab</button>
        </div>
        <div className="taskbar-list" aria-label="open windows">
          {windows.map((item) => (
            item.minimized ? (
              <button key={item.id} className="taskbar-item minimized" onClick={() => restoreWindow(item.id)}>{taskbarLabel(item.appMeta)}</button>
            ) : (
              <button key={item.id} className={`taskbar-item ${item.id === activeWindowId ? 'active' : ''}`} aria-current={item.id === activeWindowId ? 'true' : undefined} onClick={() => bringWindowToFront(item.id)}>{taskbarLabel(item.appMeta)}</button>
            )
          ))}
        </div>
        <div className="desktop-bar-status">
          {visibleWindows.length === 0 && minimizedWindows.length > 0 && <span className="desktop-status">all windows minimized</span>}
          <span className={`desktop-status ${appState === 'error' ? 'message-error' : ''}`}>{barStatus}</span>
        </div>
      </footer>

      {startOpen && (
        <nav className="start-panel" aria-label="foxlab apps">
          <div className="start-panel-title">apps</div>
          <div className="start-panel-list">
            {apps.length === 0 ? (
              <span className="start-panel-empty">no apps</span>
            ) : apps.map((meta) => (
              <button key={meta.id} className="start-panel-item" onClick={() => openApp(meta)}>
                <span>{taskbarLabel(meta)}</span>
                <span>{meta.state || 'stopped'}</span>
              </button>
            ))}
          </div>
        </nav>
      )}
    </div>
  );
}

const defaultAppMeta = {
  id: '',
  name: 'No Apps',
  windowTitle: 'No Apps',
  icon: { type: 'glyph', value: '?' },
};

const resizeEdges = ['n', 'e', 's', 'w', 'ne', 'se', 'sw', 'nw'];

function normalizeApps(items) {
  if (!Array.isArray(items)) return [];
  return items.map((item) => ({
    ...appMetaFromStatus(item),
    state: item.state || 'stopped',
  })).filter((item) => item.id);
}

function updateAppMeta(apps, meta, state) {
  if (!meta.id) return apps;
  if (apps.some((item) => item.id === meta.id)) {
    return apps.map((item) => (
      item.id === meta.id ? { ...item, ...meta, state: state || item.state } : item
    ));
  }
  return [...apps, { ...meta, state: state || 'running' }];
}

function desktopIconPosition(index) {
  const row = index % 6;
  const column = Math.floor(index / 6);
  return {
    left: 18 + column * 104,
    top: 18 + row * 82,
  };
}

function windowFromDetail(detail = {}, index = 0) {
  const id = windowIdFromDetail(detail);
  return {
    id,
    appMeta: appMetaFromWindow(detail),
    url: windowURL(detail),
    rect: defaultWindowRect(index),
    maximized: false,
    minimized: false,
    z: 10 + index,
  };
}

function windowIdFromDetail(detail = {}) {
  return [detail.appId || 'app', detail.host || '127.0.0.1', detail.port || '0', detail.path || '/'].join(':');
}

function upsertWindow(windows, nextWindow) {
  const z = nextZ(windows);
  if (windows.some((item) => item.id === nextWindow.id)) {
    return windows.map((item) => (
      item.id === nextWindow.id
        ? { ...item, ...nextWindow, rect: item.rect, maximized: item.maximized, minimized: false, z }
        : item
    ));
  }
  return [...windows, { ...nextWindow, z }];
}

function nextZ(windows) {
  return Math.max(9, ...windows.map((item) => item.z || 0)) + 1;
}

function FileIcon({ entry }) {
  if (entry.type === 'dir') {
    return (
      <span className="desktop-icon-art desktop-icon-folder" aria-hidden="true">
        <span className="folder-tab" />
        <span className="folder-body" />
        <span className="folder-line" />
      </span>
    );
  }
  if (isLabFile(entry.name)) {
    return (
      <span className="desktop-icon-art desktop-icon-lab" aria-hidden="true">
        <svg className="lab-svg" viewBox="0 0 40 34" focusable="false">
          <path className="lab-paper" d="M10.5 3.5H24.5L30.5 9.5V31.5H10.5Z" />
          <path className="lab-fold" d="M24.5 3.5V9.5H30.5" />
          <text className="lab-mark" x="20.5" y="21">LAB</text>
        </svg>
      </span>
    );
  }
  if (isDiskImage(entry.name)) {
    return (
      <span className="desktop-icon-art desktop-icon-disk" aria-hidden="true">
        <span className="disk-platter" />
        <span className="disk-line" />
        <span className="disk-dot" />
      </span>
    );
  }
  return (
    <span className="desktop-icon-art desktop-icon-file" aria-hidden="true">
      <span className="file-page" />
      <span className="file-fold" />
      <span className="file-line line-one" />
      <span className="file-line line-two" />
    </span>
  );
}

function appMetaFromStatus(data = {}) {
  return {
    id: data.id || defaultAppMeta.id,
    name: data.name || defaultAppMeta.name,
    windowTitle: data.windowTitle || data.name || defaultAppMeta.windowTitle,
    icon: data.icon?.type && data.icon?.value ? data.icon : defaultAppMeta.icon,
  };
}

function appMetaFromWindow(detail = {}) {
  return {
    id: detail.appId || defaultAppMeta.id,
    name: detail.name || defaultAppMeta.name,
    windowTitle: detail.title || detail.name || defaultAppMeta.windowTitle,
    icon: detail.icon?.type && detail.icon?.value ? detail.icon : defaultAppMeta.icon,
  };
}

function taskbarLabel(meta) {
  return (meta.id || meta.name || 'app').toLowerCase().replaceAll(' ', '-');
}

function isDiskImage(name) {
  return /\.(qcow2?|img|raw|vmdk)$/i.test(name || '');
}

function isLabFile(name) {
  return /\.lab$/i.test(name || '');
}

function windowURL(detail) {
  const path = detail.path?.startsWith('/') ? detail.path : `/${detail.path || ''}`;
  return `http://${detail.host}:${detail.port}${path}`;
}

function defaultWindowRect(index = 0) {
  const offset = Math.min(index * 34, 160);
  return clampWindowRect({
    x: 24 + offset,
    y: 24 + offset,
    width: Math.min(1560, window.innerWidth - 48),
    height: window.innerHeight - 72,
  });
}

function clampWindowRect(rect) {
  const minWidth = Math.min(720, window.innerWidth - 16);
  const minHeight = Math.min(420, window.innerHeight - 44);
  const width = Math.min(Math.max(minWidth, rect.width), Math.max(minWidth, window.innerWidth - 16));
  const height = Math.min(Math.max(minHeight, rect.height), Math.max(minHeight, window.innerHeight - 44));
  const visibleHandleWidth = Math.min(96, Math.max(40, width / 3));
  const minX = Math.min(8, visibleHandleWidth - width);
  const maxX = Math.max(8, window.innerWidth - visibleHandleWidth);
  const minY = -12;
  const maxY = Math.max(8, window.innerHeight - 64);
  return {
    x: Math.min(Math.max(minX, rect.x), maxX),
    y: Math.min(Math.max(minY, rect.y), maxY),
    width,
    height,
  };
}

function resizeWindowRect(origin, dx, dy, edges) {
  const minWidth = Math.min(720, window.innerWidth - 16);
  const minHeight = Math.min(420, window.innerHeight - 44);
  const next = { ...origin };

  if (edges.includes('e')) {
    next.width = Math.max(minWidth, origin.width + dx);
  }
  if (edges.includes('s')) {
    next.height = Math.max(minHeight, origin.height + dy);
  }
  if (edges.includes('w')) {
    next.width = Math.max(minWidth, origin.width - dx);
    next.x = origin.x + origin.width - next.width;
  }
  if (edges.includes('n')) {
    next.height = Math.max(minHeight, origin.height - dy);
    next.y = origin.y + origin.height - next.height;
  }

  return clampWindowRect(next);
}

async function apiJSON(url, options) {
  let res;
  try {
    res = await fetch(url, options);
  } catch (err) {
    throw new Error(err.message || 'Network error');
  }
  let data = null;
  try {
    data = await res.json();
  } catch {
    data = null;
  }
  if (!res.ok) {
    throw new Error(data?.error || `${res.status} ${res.statusText}`);
  }
  return data;
}

createRoot(document.getElementById('root')).render(<App />);
