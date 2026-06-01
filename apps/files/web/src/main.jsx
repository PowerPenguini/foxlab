import React, { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

function App() {
  const [path, setPath] = useState(() => initialPath());
  const [pathInput, setPathInput] = useState(() => initialPath());
  const [listing, setListing] = useState(null);
  const [selected, setSelected] = useState(null);
  const [preview, setPreview] = useState(null);
  const [imageInfo, setImageInfo] = useState(null);
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const preferredPathRef = useRef('');
  const inspectTokenRef = useRef(0);

  useEffect(() => {
    const preferredPath = preferredPathRef.current;
    preferredPathRef.current = '';
    loadPath(path, preferredPath);
  }, [path]);

  useEffect(() => {
    const token = inspectTokenRef.current + 1;
    inspectTokenRef.current = token;
    inspectEntry(selected, token);
  }, [selected]);

  async function loadPath(nextPath, preferredPath = '') {
    setBusy(true);
    setMessage('');
    try {
      const data = await apiJSON(`/api/fs/list?path=${encodeURIComponent(nextPath)}`);
      setListing(data);
      setPath(data.path);
      setPathInput(data.path);
      setSelected(selectEntry(data.entries || [], preferredPath));
      setPreview(null);
      setImageInfo(null);
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

  async function inspectEntry(entry, token) {
    setPreview(null);
    setImageInfo(null);
    if (!entry || entry.type !== 'file') return;

    if (isDiskImage(entry.path)) {
      try {
        const data = await apiJSON(`/api/images/info?path=${encodeURIComponent(entry.path)}`);
        if (token === inspectTokenRef.current) setImageInfo(data);
      } catch (err) {
        if (token === inspectTokenRef.current) setImageInfo({ error: err.message });
      }
      return;
    }

    if (entry.size > 1024 * 1024) return;
    try {
      const data = await apiJSON(`/api/fs/read?path=${encodeURIComponent(entry.path)}`);
      if (token === inspectTokenRef.current) setPreview(data);
    } catch (err) {
      if (token === inspectTokenRef.current) setPreview({ error: err.message });
    }
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

  async function unmountImage(id) {
    setBusy(true);
    setMessage('');
    try {
      await apiJSON('/api/images/unmount', {
        method: 'POST',
        body: JSON.stringify({ id }),
      });
      openPath('/');
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

  const entries = listing?.entries || [];
  const mounts = listing?.mounts || [];
  const isImage = selected && isDiskImage(selected.path);
  const status = message || `${path} | ${entries.length} entries`;

  return (
    <main className="files-app" tabIndex={0} onKeyDown={handleKeyDown}>
      <header className="topbar">
        <button onClick={goParent} disabled={!listing?.parent || listing.parent === path}>..</button>
        <input value={pathInput} onChange={(event) => setPathInput(event.target.value)} onKeyDown={(event) => {
          if (event.key === 'Enter') openPath(event.currentTarget.value);
        }} />
        <button onClick={() => loadPath(path, selected?.path)} disabled={busy}>reload</button>
        {listing?.workspace && <button onClick={() => openPath(listing.workspace)}>workspace</button>}
        <button onClick={() => openPath(homePath())}>home</button>
      </header>

      <section className="workspace">
        <FileList
          entries={entries}
          selected={selected}
          onSelect={setSelected}
          onOpen={activateEntry}
        />
        <DetailPanel
          selected={selected}
          preview={preview}
          imageInfo={imageInfo}
          isImage={isImage}
          busy={busy}
          mounts={mounts}
          onMount={() => mountImage(selected)}
          onUnmount={unmountImage}
        />
      </section>

      <footer className={`status-line ${message ? 'has-message' : ''}`}>{status}</footer>
    </main>
  );
}

function FileList({ entries, selected, onSelect, onOpen }) {
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
        >
          <span className={`file-name ${entry.type}`}>{entry.type === 'dir' ? `${entry.name}/` : entry.name}</span>
          <span>{entry.type}</span>
          <span>{entry.type === 'dir' ? '' : formatBytes(entry.size)}</span>
          <span>{formatTime(entry.modified)}</span>
        </button>
      ))}
    </section>
  );
}

function DetailPanel({ selected, preview, imageInfo, isImage, busy, mounts, onMount, onUnmount }) {
  return (
    <aside className="detail-panel">
      {!selected && <p className="empty">no selection</p>}
      {selected && (
        <section className="details">
          <dl>
            <dt>name</dt><dd>{selected.name}</dd>
            <dt>path</dt><dd>{selected.path}</dd>
            <dt>type</dt><dd>{selected.type}</dd>
            <dt>size</dt><dd>{formatBytes(selected.size)}</dd>
            {selected.mode && <><dt>mode</dt><dd>{selected.mode}</dd></>}
          </dl>
          {selected.type === 'file' && (
            <a className="action" href={`/api/fs/download?path=${encodeURIComponent(selected.path)}`}>download</a>
          )}
          {isImage && <button className="action" onClick={onMount} disabled={busy}>mount read-only</button>}
        </section>
      )}

      {imageInfo?.error && <p className="error">{imageInfo.error}</p>}
      {imageInfo?.layers && (
        <section className="details">
          <div className="section-title">layers</div>
          {imageInfo.layers.map((layer, index) => (
            <div className="layer" key={layer.path}>
              <strong>{index === 0 ? 'top' : `base ${index}`}</strong>
              <span>{baseName(layer.path)}</span>
              <small>{layer.format} {formatBytes(layer.actualSize)}</small>
            </div>
          ))}
        </section>
      )}

      {preview?.error && <p className="error">{preview.error}</p>}
      {preview?.data && (
        <section className="preview">
          <pre>{preview.data}</pre>
        </section>
      )}

      {mounts.length > 0 && (
        <section className="details">
          <div className="section-title">mounts</div>
          {mounts.map((mount) => (
            <div className="mount" key={mount.id}>
              <span>
                {baseName(mount.image)}
                {mount.backend && <small>{mount.backend}</small>}
              </span>
              <button onClick={() => onUnmount(mount.id)}>unmount</button>
            </div>
          ))}
        </section>
      )}
    </aside>
  );
}

function openFile(path) {
  window.parent?.postMessage({ type: 'foxlab:open-file', path }, '*');
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

function homePath() {
  return '/home';
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

function baseName(path) {
  return (path || '').split('/').filter(Boolean).pop() || '/';
}

function formatBytes(value = 0) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  if (value < 1024 * 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MiB`;
  return `${(value / 1024 / 1024 / 1024).toFixed(1)} GiB`;
}

function formatTime(value) {
  if (!value) return '';
  return value.replace('T', ' ').replace(/\+.*/, '').replace(/Z$/, '');
}

function isEditing(target) {
  const tag = target?.tagName?.toLowerCase();
  return tag === 'input' || tag === 'textarea' || target?.isContentEditable;
}

function clamp(value, min, max) {
  return Math.max(min, Math.min(max, value));
}

createRoot(document.getElementById('root')).render(<App />);
