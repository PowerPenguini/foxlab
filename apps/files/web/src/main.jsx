import React, { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

function App() {
  const [path, setPath] = useState(() => initialPath());
  const [pathInput, setPathInput] = useState(() => initialPath());
  const [listing, setListing] = useState(null);
  const [selected, setSelected] = useState(null);
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const [contextMenu, setContextMenu] = useState(null);
  const preferredPathRef = useRef('');

  useEffect(() => {
    const preferredPath = preferredPathRef.current;
    preferredPathRef.current = '';
    loadPath(path, preferredPath);
  }, [path]);

  useEffect(() => {
    if (!contextMenu) return undefined;
    function close() {
      setContextMenu(null);
    }
    function onKeyDown(event) {
      if (event.key === 'Escape') close();
    }
    window.addEventListener('pointerdown', close);
    window.addEventListener('resize', close);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('pointerdown', close);
      window.removeEventListener('resize', close);
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [contextMenu]);

  async function loadPath(nextPath, preferredPath = '') {
    setBusy(true);
    setMessage('');
    try {
      const data = await apiJSON(`/api/fs/list?path=${encodeURIComponent(nextPath)}`);
      setListing(data);
      setPath(data.path);
      setPathInput(data.path);
      setSelected(selectEntry(data.entries || [], preferredPath));
    } catch (err) {
      setMessage(err.message);
    } finally {
      setBusy(false);
    }
  }

  function openPath(nextPath, preferredPath = '') {
    preferredPathRef.current = preferredPath;
    if (nextPath === path) {
      loadPath(nextPath, preferredPath);
      return;
    }
    setPath(nextPath);
  }

  async function mountImage(entry = selected) {
    if (!entry || !isDiskImage(entry.path)) return;
    setBusy(true);
    setMessage(`mounting ${entry.name}`);
    try {
      const mount = await apiJSON('/api/images/mount', {
        method: 'POST',
        body: JSON.stringify({ path: entry.path }),
      });
      openPath(mount.path);
      setMessage('');
    } catch (err) {
      setMessage(err.message);
    } finally {
      setBusy(false);
    }
  }

  function activateEntry(entry = selected) {
    if (!entry) return;
    if (entry.type === 'dir') {
      openPath(entry.path);
      return;
    }
    openFile(entry.path);
  }

  function goParent() {
    if (!listing?.parent || listing.parent === path) return;
    openPath(listing.parent, path);
  }

  function moveSelection(delta) {
    const entries = listing?.entries || [];
    if (entries.length === 0) return;
    const current = entries.findIndex((entry) => entry.path === selected?.path);
    const next = clamp(Math.max(0, current) + delta, 0, entries.length - 1);
    setSelected(entries[next]);
  }

  function handleKeyDown(event) {
    if (isEditing(event.target)) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      moveSelection(1);
      return;
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault();
      moveSelection(-1);
      return;
    }
    if (event.key === 'Enter') {
      event.preventDefault();
      activateEntry();
      return;
    }
    if (event.key === 'Backspace') {
      event.preventDefault();
      goParent();
      return;
    }
    if (event.key === 'r') {
      event.preventDefault();
      loadPath(path, selected?.path);
    }
  }

  function blockContextMenu(event) {
    event.preventDefault();
    setContextMenu(null);
  }

  function openEntryContextMenu(event, entry) {
    event.preventDefault();
    event.stopPropagation();
    setSelected(entry);
    const items = [
      { label: entry.type === 'dir' ? 'open folder' : 'open', action: () => activateEntry(entry) },
    ];
    if (entry.type === 'file') {
      items.push({ label: 'download', action: () => downloadFile(entry.path) });
    }
    if (isDiskImage(entry.path)) {
      items.push({ label: 'mount read-only', action: () => mountImage(entry) });
    }
    setContextMenu({
      ...contextMenuPosition(event, items.length),
      items,
    });
  }

  const entries = listing?.entries || [];
  const status = message || `${path} | ${entries.length} entries`;

  return (
    <main className="files-app" tabIndex={0} onKeyDown={handleKeyDown} onContextMenu={blockContextMenu}>
      <header className="topbar">
        <button onClick={goParent} disabled={!listing?.parent || listing.parent === path}>..</button>
        <input value={pathInput} onChange={(event) => setPathInput(event.target.value)} onKeyDown={(event) => {
          if (event.key === 'Enter') openPath(event.currentTarget.value);
        }} />
      </header>

      <section className="workspace">
        <FileList
          entries={entries}
          selected={selected}
          onSelect={setSelected}
          onOpen={activateEntry}
          onContextMenu={openEntryContextMenu}
        />
      </section>

      <footer className={`status-line ${message ? 'has-message' : ''}`}>{status}</footer>
      <ContextMenu menu={contextMenu} onClose={() => setContextMenu(null)} />
    </main>
  );
}

function FileList({ entries, selected, onSelect, onOpen, onContextMenu }) {
  return (
    <section className="file-list">
      {entries.length === 0 ? (
        <p className="empty">empty</p>
      ) : entries.map((entry) => (
        <button
          key={entry.path}
          className={`file-row ${selected?.path === entry.path ? 'selected' : ''}`}
          onClick={() => onSelect(entry)}
          onDoubleClick={() => onOpen(entry)}
          onContextMenu={(event) => onContextMenu(event, entry)}
        >
          <span className="file-mode">{entry.mode || ''}</span>
          <span className="file-links">{entry.links || 1}</span>
          <span className="file-owner">{entry.owner || ''}</span>
          <span className="file-group">{entry.group || ''}</span>
          <span className="file-size">{formatLongSize(entry.size)}</span>
          <span className="file-date">{formatListTime(entry.modified)}</span>
          <span className={`file-name ${entry.type}`}>{entry.type === 'dir' ? `${entry.name}/` : entry.name}</span>
        </button>
      ))}
    </section>
  );
}

function openFile(path) {
  window.parent?.postMessage({ type: 'foxlab:open-file', path }, '*');
}

function downloadFile(path) {
  window.location.href = `/api/fs/download?path=${encodeURIComponent(path)}`;
}

function ContextMenu({ menu, onClose }) {
  if (!menu) return null;
  return (
    <div
      className="context-menu"
      style={{ left: menu.x, top: menu.y }}
      role="menu"
      onPointerDown={(event) => event.stopPropagation()}
      onContextMenu={(event) => event.preventDefault()}
    >
      {menu.items.map((item) => (
        <button
          key={item.label}
          type="button"
          role="menuitem"
          onClick={() => {
            onClose();
            item.action();
          }}
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}

async function apiJSON(url, options = {}) {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `${res.status} ${res.statusText}`);
  return data;
}

function initialPath() {
  const value = new URLSearchParams(window.location.search).get('path');
  return value || '/';
}

function selectEntry(entries, preferredPath) {
  if (preferredPath) {
    const found = entries.find((entry) => entry.path === preferredPath);
    if (found) return found;
  }
  return entries[0] || null;
}

function isDiskImage(path) {
  return /\.(qcow2|raw|img)$/i.test(path || '');
}

function formatTime(value) {
  if (!value) return '';
  return value.replace('T', ' ').replace(/\+.*/, '').replace(/Z$/, '');
}

function formatLongSize(value = 0) {
  return String(value || 0);
}

function formatListTime(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return formatTime(value);
  const year = date.getFullYear();
  const currentYear = new Date().getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  if (year !== currentYear) {
    return `${year}-${month}-${day}`;
  }
  const hours = String(date.getHours()).padStart(2, '0');
  const minutes = String(date.getMinutes()).padStart(2, '0');
  return `${month}-${day} ${hours}:${minutes}`;
}

function isEditing(target) {
  const tag = target?.tagName?.toLowerCase();
  return tag === 'input' || tag === 'textarea' || target?.isContentEditable;
}

function contextMenuPosition(event, itemCount) {
  const width = 176;
  const height = Math.max(22, itemCount * 22 + 2);
  return {
    x: Math.max(4, Math.min(event.clientX, window.innerWidth - width - 4)),
    y: Math.max(4, Math.min(event.clientY, window.innerHeight - height - 4)),
  };
}

function clamp(value, min, max) {
  return Math.max(min, Math.min(max, value));
}

createRoot(document.getElementById('root')).render(<App />);
