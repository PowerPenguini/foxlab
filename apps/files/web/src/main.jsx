import React, { useEffect, useMemo, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

const roots = [
  { label: '/', path: '/' },
  { label: 'home', path: homePath() },
];

function App() {
  const [path, setPath] = useState('/');
  const [listing, setListing] = useState(null);
  const [selected, setSelected] = useState(null);
  const [preview, setPreview] = useState(null);
  const [images, setImages] = useState([]);
  const [imageInfo, setImageInfo] = useState(null);
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    loadPath(path);
  }, [path]);

  useEffect(() => {
    refreshImages();
  }, []);

  const effectiveRoots = useMemo(() => {
    const items = [...roots];
    if (listing?.workspace) items.splice(1, 0, { label: 'workspace', path: listing.workspace });
    for (const mount of listing?.mounts || []) {
      items.push({ label: `mnt ${baseName(mount.image)}`, path: mount.path, readOnly: true });
    }
    return dedupeRoots(items);
  }, [listing]);

  async function loadPath(nextPath) {
    setBusy(true);
    setMessage('');
    try {
      const data = await apiJSON(`/api/fs/list?path=${encodeURIComponent(nextPath)}`);
      setListing(data);
      setSelected(null);
      setPreview(null);
    } catch (err) {
      setMessage(err.message);
    } finally {
      setBusy(false);
    }
  }

  async function refreshImages() {
    try {
      const data = await apiJSON('/api/images/discover');
      setImages(data || []);
    } catch (err) {
      setMessage(err.message);
    }
  }

  async function openEntry(entry) {
    setSelected(entry);
    setPreview(null);
    setImageInfo(null);
    if (entry.type === 'dir') {
      setPath(entry.path);
      return;
    }
    if (isDiskImage(entry.path)) {
      loadImage(entry.path);
      return;
    }
    if (entry.size <= 1024 * 1024) {
      try {
        setPreview(await apiJSON(`/api/fs/read?path=${encodeURIComponent(entry.path)}`));
      } catch (err) {
        setPreview({ error: err.message });
      }
    }
  }

  async function loadImage(imagePath) {
    setMessage('');
    try {
      setImageInfo(await apiJSON(`/api/images/info?path=${encodeURIComponent(imagePath)}`));
    } catch (err) {
      setImageInfo({ error: err.message });
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
      setPath(mount.path);
      await loadPath(mount.path);
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
      await loadPath('/');
    } catch (err) {
      setMessage(err.message);
    } finally {
      setBusy(false);
    }
  }

  async function createFolder() {
    const name = window.prompt('Folder name');
    if (!name) return;
    await mutate('/api/fs/mkdir', { path: joinPath(path, name) });
  }

  async function renameSelected() {
    if (!selected) return;
    const name = window.prompt('New name', selected.name);
    if (!name || name === selected.name) return;
    await mutate('/api/fs/rename', { path: selected.path, to: joinPath(path, name) });
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
    await mutate('/api/fs/copy', { path: selected.path, to: target });
  }

  async function mutate(url, body) {
    setBusy(true);
    setMessage('');
    try {
      await apiJSON(url, { method: 'POST', body: JSON.stringify(body) });
      await loadPath(path);
    } catch (err) {
      setMessage(err.message);
    } finally {
      setBusy(false);
    }
  }

  const canWrite = listing && !listing.readOnly;
  const isImage = selected && isDiskImage(selected.path);

  return (
    <main className="files-app">
      <aside className="sidebar">
        <div className="sidebar-title">Files</div>
        <nav>
          {effectiveRoots.map((root) => (
            <button key={root.path} className={path === root.path ? 'active' : ''} onClick={() => setPath(root.path)}>
              <span>{root.label}</span>
              {root.readOnly && <em>ro</em>}
            </button>
          ))}
        </nav>
        <section>
          <div className="section-line">
            <span>images</span>
            <button onClick={refreshImages}>scan</button>
          </div>
          <div className="image-list">
            {images.slice(0, 50).map((image) => (
              <button key={image.path} onClick={() => {
                setSelected({ name: image.name, path: image.path, type: 'file', size: image.size });
                loadImage(image.path);
              }}>
                {image.name}
              </button>
            ))}
          </div>
        </section>
      </aside>

      <section className="workspace">
        <header className="toolbar">
          <button onClick={() => setPath(listing?.parent || '/')}>..</button>
          <input value={path} onChange={(event) => setPath(event.target.value)} onKeyDown={(event) => {
            if (event.key === 'Enter') loadPath(event.currentTarget.value);
          }} />
          <button onClick={() => loadPath(path)} disabled={busy}>reload</button>
          <button onClick={createFolder} disabled={!canWrite}>mkdir</button>
          <button onClick={renameSelected} disabled={!canWrite || !selected}>rename</button>
          <button onClick={copySelected} disabled={!canWrite || !selected || selected.type === 'dir'}>copy</button>
          <button onClick={deleteSelected} disabled={!canWrite || !selected}>delete</button>
        </header>
        {message && <div className="message">{message}</div>}
        <div className="table">
          <div className="row head">
            <span>Name</span>
            <span>Type</span>
            <span>Size</span>
            <span>Mode</span>
            <span>Modified</span>
          </div>
          {(listing?.entries || []).map((entry) => (
            <button key={entry.path} className={`row ${selected?.path === entry.path ? 'selected' : ''}`} onClick={() => openEntry(entry)}>
              <span>{entry.type === 'dir' ? `[${entry.name}]` : entry.name}</span>
              <span>{entry.readOnly ? `${entry.type} ro` : entry.type}</span>
              <span>{formatBytes(entry.size)}</span>
              <span>{entry.mode}</span>
              <span>{formatTime(entry.modified)}</span>
            </button>
          ))}
        </div>
      </section>

      <aside className="details">
        <div className="sidebar-title">Detail</div>
        {selected ? (
          <div className="detail-block">
            <dl>
              <dt>name</dt><dd>{selected.name}</dd>
              <dt>path</dt><dd>{selected.path}</dd>
              <dt>size</dt><dd>{formatBytes(selected.size)}</dd>
              <dt>type</dt><dd>{selected.type}</dd>
            </dl>
            {selected.type === 'file' && (
              <a className="link-button" href={`/api/fs/download?path=${encodeURIComponent(selected.path)}`}>download</a>
            )}
            {isImage && <button onClick={() => mountImage(selected.path)} disabled={busy}>mount read-only</button>}
          </div>
        ) : (
          <p className="empty">no selection</p>
        )}

        {imageInfo?.error && <p className="error">{imageInfo.error}</p>}
        {imageInfo?.layers && (
          <section className="detail-block">
            <h2>layers</h2>
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
            <h2>preview</h2>
            <pre>{preview.data}</pre>
          </section>
        )}

        {(listing?.mounts || []).length > 0 && (
          <section className="detail-block">
            <h2>mounts</h2>
            {listing.mounts.map((mount) => (
              <div className="mount" key={mount.id}>
                <span>{baseName(mount.image)}</span>
                <button onClick={() => unmountImage(mount.id)}>unmount</button>
              </div>
            ))}
          </section>
        )}
      </aside>
    </main>
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

function homePath() {
  return '/home';
}

function dedupeRoots(items) {
  const seen = new Set();
  return items.filter((item) => {
    if (!item.path || seen.has(item.path)) return false;
    seen.add(item.path);
    return true;
  });
}

function isDiskImage(path) {
  return /\.(qcow2|raw|img)$/i.test(path || '');
}

function baseName(path) {
  return (path || '').split('/').filter(Boolean).pop() || '/';
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

createRoot(document.getElementById('root')).render(<App />);
