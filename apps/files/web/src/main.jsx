import React, { useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

const roots = [
  { label: '/', path: '/' },
  { label: 'home', path: homePath() },
];

function App() {
  const [path, setPath] = useState(() => initialPath());
  const [pathInput, setPathInput] = useState(() => initialPath());
  const [listing, setListing] = useState(null);
  const [parentListing, setParentListing] = useState(null);
  const [childListing, setChildListing] = useState(null);
  const [selected, setSelected] = useState(null);
  const [preview, setPreview] = useState(null);
  const [images, setImages] = useState([]);
  const [imageInfo, setImageInfo] = useState(null);
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const appRef = useRef(null);
  const preferredPathRef = useRef('');
  const inspectTokenRef = useRef(0);

  useEffect(() => {
    const preferredPath = preferredPathRef.current;
    preferredPathRef.current = '';
    loadPath(path, preferredPath);
  }, [path]);

  useEffect(() => {
    refreshImages();
  }, []);

  useEffect(() => {
    if (!selected) {
      setPreview(null);
      setImageInfo(null);
      setChildListing(null);
      return;
    }
    const token = inspectTokenRef.current + 1;
    inspectTokenRef.current = token;
    inspectEntry(selected, token);
  }, [selected]);

  const effectiveRoots = useMemo(() => {
    const items = [...roots];
    if (listing?.workspace) items.splice(1, 0, { label: 'workspace', path: listing.workspace });
    for (const mount of listing?.mounts || []) {
      items.push({ label: `mnt ${baseName(mount.image)}`, path: mount.path, readOnly: true });
    }
    return dedupeRoots(items);
  }, [listing]);

  async function loadPath(nextPath, preferredPath = '') {
    setBusy(true);
    setMessage('');
    try {
      const data = await listPath(nextPath);
      setListing(data);
      setPath(data.path);
      setPathInput(data.path);
      setSelected(selectInitialEntry(data.entries || [], preferredPath));
      setPreview(null);
      setImageInfo(null);
      setChildListing(null);
      if (data.parent && data.parent !== data.path) {
        listPath(data.parent).then(setParentListing).catch(() => setParentListing(null));
      } else {
        setParentListing(null);
      }
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

  async function refreshImages() {
    try {
      const data = await apiJSON('/api/images/discover');
      setImages(data || []);
    } catch (err) {
      setMessage(err.message);
    }
  }

  function chooseEntry(entry) {
    setSelected(entry);
    appRef.current?.focus();
  }

  async function inspectEntry(entry, token) {
    setPreview(null);
    setImageInfo(null);
    setChildListing(null);
    if (entry.type === 'dir') {
      try {
        const data = await listPath(entry.path);
        if (token === inspectTokenRef.current) setChildListing(data);
      } catch (err) {
        if (token === inspectTokenRef.current) setChildListing({ error: err.message, entries: [] });
      }
      return;
    }
    if (isDiskImage(entry.path)) {
      loadImage(entry.path, token);
      return;
    }
    if (entry.size <= 1024 * 1024) {
      try {
        const data = await apiJSON(`/api/fs/read?path=${encodeURIComponent(entry.path)}`);
        if (token === inspectTokenRef.current) setPreview(data);
      } catch (err) {
        if (token === inspectTokenRef.current) setPreview({ error: err.message });
      }
    }
  }

  function activateEntry(entry = selected) {
    if (!entry) return;
    if (entry.type === 'dir') {
      openPath(entry.path);
      return;
    }
    if (isDiskImage(entry.path)) {
      mountImage(entry.path);
    }
  }

  function moveSelection(delta) {
    const entries = listing?.entries || [];
    if (entries.length === 0) return;
    const index = Math.max(0, entries.findIndex((entry) => entry.path === selected?.path));
    const nextIndex = clamp(index + delta, 0, entries.length - 1);
    setSelected(entries[nextIndex]);
  }

  function handleKeyDown(event) {
    if (isEditing(event.target)) return;
    if (event.key === 'ArrowDown' || event.key === 'j') {
      event.preventDefault();
      moveSelection(1);
      return;
    }
    if (event.key === 'ArrowUp' || event.key === 'k') {
      event.preventDefault();
      moveSelection(-1);
      return;
    }
    if (event.key === 'ArrowRight' || event.key === 'l' || event.key === 'Enter') {
      event.preventDefault();
      activateEntry();
      return;
    }
    if (event.key === 'ArrowLeft' || event.key === 'h' || event.key === 'Backspace') {
      event.preventDefault();
      goParent();
      return;
    }
    if (event.key === 'r') {
      event.preventDefault();
      loadPath(path, selected?.path);
    }
  }

  async function loadImage(imagePath, token = inspectTokenRef.current) {
    setMessage('');
    try {
      const data = await apiJSON(`/api/images/info?path=${encodeURIComponent(imagePath)}`);
      if (token === inspectTokenRef.current) setImageInfo(data);
    } catch (err) {
      if (token === inspectTokenRef.current) setImageInfo({ error: err.message });
    }
  }

  async function mountImage(imagePath) {
    setBusy(true);
    setMessage(`mounting ${baseName(imagePath)}`);
    try {
      const mount = await apiJSON('/api/images/mount', {
        method: 'POST',
        body: JSON.stringify({ path: imagePath }),
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

  async function createFolder() {
    const name = window.prompt('Folder name');
    if (!name) return;
    await mutate('/api/fs/mkdir', { path: joinPath(path, name) }, joinPath(path, name));
  }

  async function renameSelected() {
    if (!selected) return;
    const name = window.prompt('New name', selected.name);
    if (!name || name === selected.name) return;
    const nextPath = joinPath(path, name);
    await mutate('/api/fs/rename', { path: selected.path, to: nextPath }, nextPath);
  }

  async function deleteSelected() {
    if (!selected) return;
    if (!window.confirm(`Delete ${selected.name}?`)) return;
    await mutate('/api/fs/delete', { path: selected.path });
  }

  async function copySelected() {
    if (!selected) return;
    const target = window.prompt('Copy to absolute path', joinPath(path, `copy-${selected.name}`));
    if (!target) return;
    await mutate('/api/fs/copy', { path: selected.path, to: target }, target);
  }

  async function mutate(url, body, preferredPath = selected?.path) {
    setBusy(true);
    setMessage('');
    try {
      await apiJSON(url, { method: 'POST', body: JSON.stringify(body) });
      await loadPath(path, preferredPath);
    } catch (err) {
      setMessage(err.message);
    } finally {
      setBusy(false);
    }
  }

  function goParent() {
    if (!listing?.parent || listing.parent === path) return;
    openPath(listing.parent, path);
  }

  const canWrite = listing && !listing.readOnly;
  const isImage = selected && isDiskImage(selected.path);
  const parentEntries = parentListing?.entries || [];
  const childEntries = childListing?.entries || [];

  return (
    <main className="files-app" tabIndex={0} onKeyDown={handleKeyDown} ref={appRef}>
      <header className="topbar">
        <button onClick={goParent} disabled={!listing?.parent || listing.parent === path}>..</button>
        <input value={pathInput} onChange={(event) => setPathInput(event.target.value)} onKeyDown={(event) => {
          if (event.key === 'Enter') openPath(event.currentTarget.value);
        }} />
        <button onClick={() => loadPath(path, selected?.path)} disabled={busy}>reload</button>
        <button onClick={createFolder} disabled={!canWrite}>mkdir</button>
        <button onClick={renameSelected} disabled={!canWrite || !selected}>rename</button>
        <button onClick={copySelected} disabled={!canWrite || !selected || selected.type === 'dir'}>copy</button>
        <button onClick={deleteSelected} disabled={!canWrite || !selected}>delete</button>
      </header>

      <section className="ranger-grid">
        <aside className="nav-column">
          <div className="column-title">places</div>
          <div className="root-list">
            {effectiveRoots.map((root) => (
              <button key={root.path} className={path === root.path ? 'active' : ''} onClick={() => openPath(root.path)}>
                <span>{root.label}</span>
                {root.readOnly && <em>ro</em>}
              </button>
            ))}
          </div>
          <div className="column-title">images</div>
          <div className="root-list image-list">
            {images.slice(0, 80).map((image) => (
              <button key={image.path} onClick={() => {
                openPath(parentPath(image.path), image.path);
              }}>
                {image.name}
              </button>
            ))}
          </div>
        </aside>

        <FileColumn
          title={baseName(parentListing?.path || listing?.parent || '')}
          entries={parentEntries}
          activePath={path}
          emptyText="/"
          onChoose={(entry) => entry.type === 'dir' && openPath(entry.path)}
          dim
        />

        <FileColumn
          title={baseName(path)}
          entries={listing?.entries || []}
          activePath={selected?.path}
          emptyText="empty"
          onChoose={chooseEntry}
          onActivate={activateEntry}
        />

        <PreviewColumn
          selected={selected}
          childEntries={childEntries}
          childError={childListing?.error}
          preview={preview}
          imageInfo={imageInfo}
          isImage={isImage}
          busy={busy}
          mounts={listing?.mounts || []}
          onMount={() => selected && mountImage(selected.path)}
          onUnmount={unmountImage}
        />
      </section>

      <footer className={`status-line ${message ? 'has-message' : ''}`}>
        <span>{message || statusText(path, selected, listing)}</span>
      </footer>
    </main>
  );
}

function FileColumn({ title, entries, activePath, emptyText, onChoose, onActivate, dim = false }) {
  return (
    <section className={`file-column ${dim ? 'dim' : ''}`}>
      <div className="column-title">{title || '/'}</div>
      <div className="entry-list">
        {entries.length === 0 ? (
          <p className="empty">{emptyText}</p>
        ) : entries.map((entry) => (
          <button
            key={entry.path}
            className={`entry-row ${activePath === entry.path ? 'selected' : ''}`}
            onClick={() => onChoose?.(entry)}
            onDoubleClick={() => onActivate?.(entry)}
          >
            <span className={`entry-name ${entry.type}`}>{entry.type === 'dir' ? `${entry.name}/` : entry.name}</span>
            <span className="entry-size">{entry.type === 'dir' ? '' : formatBytes(entry.size)}</span>
          </button>
        ))}
      </div>
    </section>
  );
}

function PreviewColumn({ selected, childEntries, childError, preview, imageInfo, isImage, busy, mounts, onMount, onUnmount }) {
  return (
    <aside className="preview-column">
      <div className="column-title">{selected ? baseName(selected.path) : 'preview'}</div>
      {!selected && <p className="empty">no selection</p>}
      {selected?.type === 'dir' && (
        <div className="entry-list preview-list">
          {childError && <p className="error">{childError}</p>}
          {!childError && childEntries.length === 0 && <p className="empty">empty</p>}
          {childEntries.slice(0, 120).map((entry) => (
            <div className="entry-row ghost" key={entry.path}>
              <span className={`entry-name ${entry.type}`}>{entry.type === 'dir' ? `${entry.name}/` : entry.name}</span>
              <span className="entry-size">{entry.type === 'dir' ? '' : formatBytes(entry.size)}</span>
            </div>
          ))}
        </div>
      )}
      {selected?.type === 'file' && (
        <div className="details-block">
          <dl>
            <dt>name</dt><dd>{selected.name}</dd>
            <dt>path</dt><dd>{selected.path}</dd>
            <dt>size</dt><dd>{formatBytes(selected.size)}</dd>
            <dt>type</dt><dd>{selected.type}</dd>
            {selected.mode && <><dt>mode</dt><dd>{selected.mode}</dd></>}
            {selected.modified && <><dt>mtime</dt><dd>{formatTime(selected.modified)}</dd></>}
          </dl>
          <a className="action-link" href={`/api/fs/download?path=${encodeURIComponent(selected.path)}`}>download</a>
          {isImage && <button className="action-link" onClick={onMount} disabled={busy}>mount read-only</button>}
        </div>
      )}
      {imageInfo?.error && <p className="error">{imageInfo.error}</p>}
      {imageInfo?.layers && (
        <section className="details-block">
          <div className="column-title inline">layers</div>
          {imageInfo.source && <p className="meta-line">source {imageInfo.source}</p>}
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
        <section className="text-preview">
          <pre>{preview.data}</pre>
        </section>
      )}
      {mounts.length > 0 && (
        <section className="details-block mounts-block">
          <div className="column-title inline">mounts</div>
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

async function apiJSON(url, options = {}) {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `${res.status} ${res.statusText}`);
  return data;
}

function listPath(path) {
  return apiJSON(`/api/fs/list?path=${encodeURIComponent(path)}`);
}

function homePath() {
  return '/home';
}

function initialPath() {
  const value = new URLSearchParams(window.location.search).get('path');
  return value || '/';
}

function dedupeRoots(items) {
  const seen = new Set();
  return items.filter((item) => {
    if (!item.path || seen.has(item.path)) return false;
    seen.add(item.path);
    return true;
  });
}

function selectInitialEntry(entries, preferredPath) {
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

function parentPath(path) {
  if (!path || path === '/') return '/';
  const parts = path.split('/').filter(Boolean);
  parts.pop();
  return parts.length ? `/${parts.join('/')}` : '/';
}

function joinPath(dir, name) {
  if (dir === '/') return `/${name}`;
  return `${dir.replace(/\/$/, '')}/${name}`;
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

function statusText(path, selected, listing) {
  const count = listing?.entries?.length ?? 0;
  const head = selected ? `${selected.type} ${selected.name}` : `${count} entries`;
  return `${path} | ${head}`;
}

function isEditing(target) {
  const tag = target?.tagName?.toLowerCase();
  return tag === 'input' || tag === 'textarea' || target?.isContentEditable;
}

function clamp(value, min, max) {
  return Math.max(min, Math.min(max, value));
}

createRoot(document.getElementById('root')).render(<App />);
