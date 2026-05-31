import React, { useEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import RFB from '@novnc/novnc';
import './styles.css';

function App() {
  const viewerRef = useRef(null);
  const rfbRef = useRef(null);
  const [label, setLabel] = useState('VNC');
  const [status, setStatus] = useState('connecting');
  const [error, setError] = useState('');

  useEffect(() => {
    let disposed = false;

    async function connect() {
      try {
        const res = await fetch('/api/console');
        const data = await res.json();
        if (!res.ok) {
          throw new Error(data.error || 'Console is not available');
        }
        if (disposed) {
          return;
        }
        if (data.label) {
          setLabel(data.label);
        }
        const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const path = data.path || '/api/vnc/ws';
        const url = `${scheme}//${window.location.host}${path}`;
        const rfb = new RFB(viewerRef.current, url);
        rfb.scaleViewport = true;
        rfb.resizeSession = false;
        rfb.background = '#050706';
        rfb.addEventListener('connect', () => setStatus('connected'));
        rfb.addEventListener('disconnect', (event) => {
          setStatus('disconnected');
          if (event.detail?.clean === false) {
            setError('Connection closed');
          }
        });
        rfb.addEventListener('securityfailure', (event) => {
          setError(event.detail?.reason || 'VNC security failed');
        });
        rfbRef.current = rfb;
      } catch (err) {
        if (!disposed) {
          setStatus('error');
          setError(err.message);
        }
      }
    }

    connect();
    return () => {
      disposed = true;
      rfbRef.current?.disconnect();
      rfbRef.current = null;
    };
  }, []);

  return (
    <main className="viewer-shell">
      <header className="viewer-bar">
        <div className="viewer-title">{label}</div>
        <div className={`viewer-status ${status}`}>{status}</div>
      </header>
      <section className="viewer-frame">
        <div ref={viewerRef} className="viewer-canvas" />
        {error && <div className="viewer-error">{error}</div>}
      </section>
    </main>
  );
}

createRoot(document.getElementById('root')).render(<App />);
