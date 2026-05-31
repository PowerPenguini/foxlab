import React, { useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

const blankLab = {
  id: '',
  name: '',
  vms: [],
  switches: [],
  externalLinks: [],
  disks: [],
  layout: { nodes: {}, links: [] },
};
const gridSize = 16;
const nodeWidth = 192;
const nodeHeight = 48;

function App() {
  const [labs, setLabs] = useState([]);
  const [activeId, setActiveId] = useState('');
  const [lab, setLab] = useState(blankLab);
  const [isos, setISOs] = useState([]);
  const [networkInterfaces, setNetworkInterfaces] = useState([]);
  const [selected, setSelected] = useState(null);
  const [status, setStatus] = useState(null);
  const [message, setMessage] = useState('');
  const [messageKind, setMessageKind] = useState('info');
  const [commandMode, setCommandMode] = useState(false);
  const [commandText, setCommandText] = useState('');
  const commandInputRef = useRef(null);

  function showMessage(text, kind = 'info') {
    setMessage(text);
    setMessageKind(kind);
  }

  useEffect(() => {
    refreshLabs();
    refreshISOs();
    refreshNetworkInterfaces();
  }, []);

  useEffect(() => {
    if (activeId && labs.some((item) => item.id === activeId)) {
      loadLab(activeId);
    }
  }, [activeId, labs]);

  useEffect(() => {
    if (commandMode) {
      commandInputRef.current?.focus();
    }
  }, [commandMode]);

  useEffect(() => {
    function onKeyDown(event) {
      const tag = event.target?.tagName;
      const isEditing = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || event.target?.isContentEditable;
      if (!commandMode && !isEditing && event.key === ':') {
        event.preventDefault();
        openCommandMode();
      }
      if (!commandMode && !isEditing && (event.key === 'Delete' || event.key === 'Backspace') && selected) {
        event.preventDefault();
        deleteSelectedNode();
      }
      if (commandMode && event.key === 'Escape') {
        event.preventDefault();
        closeCommandMode();
      }
    }
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [commandMode, selected, lab]);

  async function refreshLabs() {
    try {
      const data = await apiJSON('/api/labs');
      const nextLabs = Array.isArray(data) ? data : [];
      setLabs(nextLabs);
      if (!activeId && nextLabs.length > 0) {
        setActiveId(nextLabs[0].id);
      }
    } catch (err) {
      showMessage(err.message, 'error');
    }
  }

  async function loadLab(id) {
    try {
      const data = await apiJSON(`/api/labs/${id}`);
      setLab(normalizeLab(data));
      setSelected(null);
      showMessage('');
      refreshStatus(id);
    } catch (err) {
      showMessage(err.message, 'error');
    }
  }

  async function saveLab(next = lab, options = {}) {
    const target = ensureLabID(next, labs);
    try {
      const data = await apiJSON(`/api/labs/${target.id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(target),
      });
      setLab(normalizeLab(data));
      setActiveId(data.id);
      if (!options.silent) {
        showMessage('Saved');
      }
      refreshLabs();
      return normalizeLab(data);
    } catch (err) {
      showMessage(err.message, 'error');
      return null;
    }
  }

  async function runAction(action) {
    let target = lab;
    if (action === 'apply') {
      const saved = await saveLab(lab, { silent: true });
      if (!saved) return;
      target = saved;
    }
    showMessage(`${action} started`);
    try {
      const data = await apiJSON(`/api/labs/${target.id}/${action}`, { method: 'POST' });
      showMessage(data.status || `${action} done`);
      refreshStatus(target.id);
    } catch (err) {
      showMessage(err.message, 'error');
      refreshStatus(target.id);
    }
  }

  async function refreshISOs() {
    try {
      const data = await apiJSON('/api/isos');
      setISOs(Array.isArray(data) ? data : []);
    } catch {
      setISOs([]);
    }
  }

  async function refreshNetworkInterfaces() {
    try {
      const data = await apiJSON('/api/network-interfaces');
      setNetworkInterfaces(Array.isArray(data) ? data : []);
    } catch {
      setNetworkInterfaces([]);
    }
  }

  async function refreshStatus(id = lab.id) {
    if (!id) return;
    try {
      const data = await apiJSON(`/api/labs/${id}/status`);
      setStatus(normalizeStatus(data));
    } catch (err) {
      setStatus(normalizeStatus(null));
      showMessage(err.message, 'error');
    }
  }

  function newLab() {
    const next = nextLabDraft(labs);
    setLab(next);
    setActiveId(next.id);
    setSelected(null);
    setStatus(normalizeStatus(null));
    showMessage('New lab draft');
  }

  function openCommandMode() {
    setCommandMode(true);
    setCommandText('');
  }

  function closeCommandMode() {
    setCommandMode(false);
    setCommandText('');
  }

  async function submitCommand(event) {
    event.preventDefault();
    const command = commandText.trim().toLowerCase();
    closeCommandMode();
    if (!command) return;
    if (command === 'w' || command === 'write' || command === 'save' || command === 'apply') {
      await runAction('apply');
    } else if (command === 'destroy') {
      if (selected) {
        await deleteSelectedNode();
      } else {
        await runAction('destroy');
      }
    } else if (command === 'delete' || command === 'rm') {
      await deleteSelectedNode();
    } else if (command === 'mk-vm' || command === 'mk vm' || command === 'vm') {
      await addVM();
    } else if (command === 'mk-switch' || command === 'mk switch' || command === 'switch' || command === 'sw') {
      addSwitch();
    } else if (command === 'mk-link' || command === 'mk link' || command === 'link' || command === 'external') {
      addExternalLink();
    } else if (command === 'new-lab' || command === 'new') {
      newLab();
    } else if (command === 'status') {
      await refreshStatus(lab.id);
      showMessage('Status refreshed');
    } else {
      showMessage(`Not an editor command: ${command}`, 'error');
    }
  }

  async function addVM() {
    const target = ensureLabID(lab, labs);
    const id = uniqueId('vm', (target.vms || []).map((vm) => vm.id));
    const vm = {
      id,
      name: id,
      memoryMB: 2048,
      cpus: 2,
      disk: `labs/${target.id}/disks/${id}.qcow2`,
      iso: preferredISO(isos),
      vnc: true,
      networks: [],
    };
    const next = ensureDiskForVM({
      ...target,
      vms: [...(target.vms || []), vm],
      layout: putNode(target.layout, id, 120 + (target.vms || []).length * 40, 140 + (target.vms || []).length * 30),
    }, vm);
    setLab(next);
    setActiveId(next.id);
    setSelected({ type: 'vm', id: vm.id });
    showMessage(`Drafted ${vm.id}`);
  }

  async function deleteVM(id) {
    deleteLocalNode({ type: 'vm', id });
  }

  async function deleteSwitch(id) {
    deleteLocalNode({ type: 'switch', id });
  }

  function deleteLocalNode(node) {
    const next = removeNodeFromLab(lab, node);
    setLab(next);
    setSelected(null);
    showMessage(`Deleted ${node.id}`);
  }

  async function deleteSelectedNode() {
    if (!selected) return;
    if (selected.type === 'vm') {
      await deleteVM(selected.id);
      return;
    }
    if (selected.type === 'switch') {
      await deleteSwitch(selected.id);
      return;
    }
    if (selected.type === 'external') {
      deleteExternalLink(selected.id);
    }
  }

  async function destroyFromToolbar() {
    if (selected) {
      await deleteSelectedNode();
      return;
    }
    await runAction('destroy');
  }

  function addSwitch() {
    const id = uniqueId('sw', (lab.switches || []).map((sw) => sw.id));
    const next = {
      ...lab,
      switches: [...(lab.switches || []), { id, name: id, mode: 'bridge' }],
      layout: putNode(lab.layout, id, 360 + (lab.switches || []).length * 30, 180),
    };
    setLab(next);
    setSelected({ type: 'switch', id });
  }

  function addExternalLink() {
    const id = uniqueId('link', (lab.externalLinks || []).map((link) => link.id));
    const next = {
      ...lab,
      externalLinks: [...(lab.externalLinks || []), { id, name: id, interface: '' }],
      layout: putNode(lab.layout, id, 560 + (lab.externalLinks || []).length * 30, 180),
    };
    setLab(next);
    setSelected({ type: 'external', id });
  }

  function deleteExternalLink(id) {
    deleteLocalNode({ type: 'external', id });
  }

  function connectNodes(from, to) {
    if (!from || !to || sameEndpoint(from, to)) return;
    const vmNode = [from, to].find((node) => node.type === 'vm');
    const switchNode = [from, to].find((node) => node.type === 'switch');
    const externalNode = [from, to].find((node) => node.type === 'external');
    let next = { ...lab, layout: addLayoutLink(lab.layout, from, to) };
    if (vmNode && switchNode) {
      next.vms = (lab.vms || []).map((vm) => {
        if (vm.id !== vmNode.id) return vm;
        if ((vm.networks || []).some((nic) => nic.switch === switchNode.id)) return vm;
        return { ...vm, networks: [...(vm.networks || []), { switch: switchNode.id }] };
      });
    }
    if (vmNode && externalNode) {
      next.vms = (next.vms || lab.vms || []).map((vm) => {
        if (vm.id !== vmNode.id) return vm;
        if ((vm.networks || []).some((nic) => nic.externalLink === externalNode.id)) return vm;
        return { ...vm, networks: [...(vm.networks || []), { externalLink: externalNode.id }] };
      });
    }
    if (switchNode && externalNode) {
      next = {
        ...next,
        switches: (lab.switches || []).map((sw) => (
          sw.id === switchNode.id ? { ...sw, mode: sw.mode === 'nat' ? 'nat' : 'bridge', externalLink: externalNode.id } : sw
        )),
        layout: addLayoutLink(removeSwitchExternalLinks(next.layout, switchNode.id), from, to),
      };
    }
    setLab(next);
  }

  function moveNode(id, x, y) {
    setLab((current) => ({ ...current, layout: putNode(current.layout, id, x, y) }));
  }

  const selectedObject = useMemo(() => {
    if (!selected) return null;
    if (selected.type === 'vm') return (lab.vms || []).find((vm) => vm.id === selected.id);
    if (selected.type === 'switch') return (lab.switches || []).find((sw) => sw.id === selected.id);
    if (selected.type === 'external') return (lab.externalLinks || []).find((link) => link.id === selected.id);
    return null;
  }, [selected, lab]);

  return (
    <div className="app topology-root">
      <aside className="sidebar">
        <div className="sidebar-body">
          <button onClick={newLab}>new lab</button>
          <div className="terminal-label">workspace</div>
          <div className="lab-list">
            {labs.map((item) => (
              <button key={item.id} className={item.id === lab.id ? 'active' : ''} onClick={() => setActiveId(item.id)}>
                {item.name || item.id}
              </button>
            ))}
          </div>
        </div>
      </aside>

      <main className="workspace">
        <section className="stage">
          <header className="toolbar">
            <div className="command-line">
              <span>lab.name=</span>
              <input value={lab.name || ''} onChange={(e) => setLab({ ...lab, name: e.target.value })} />
            </div>
            <nav>
              <button onClick={addVM}>./mk-vm</button>
              <button onClick={addSwitch}>./mk-switch</button>
              <button onClick={addExternalLink}>./mk-link</button>
              <button onClick={() => runAction('apply')}>./apply</button>
              <button onClick={destroyFromToolbar}>./destroy</button>
            </nav>
          </header>
          <Topology lab={lab} status={status} selected={selected} setSelected={setSelected} moveNode={moveNode} connectNodes={connectNodes} />
        </section>
        <StatusLine
          mode={commandMode ? 'command' : 'normal'}
          commandMode={commandMode}
          commandText={commandText}
          setCommandText={setCommandText}
          commandInputRef={commandInputRef}
          onCommandOpen={openCommandMode}
          onCommandSubmit={submitCommand}
          messageKind={messageKind}
          message={message || `${(lab.vms || []).length} vm / ${(lab.switches || []).length} switch / ${(lab.externalLinks || []).length} link`}
        />
      </main>

      <aside className="inspector">
        <Inspector lab={lab} setLab={setLab} selected={selected} object={selectedObject} status={status} isos={isos} networkInterfaces={networkInterfaces} />
      </aside>
    </div>
  );
}

function StatusLine({ mode, commandMode, commandText, setCommandText, commandInputRef, onCommandOpen, onCommandSubmit, message, messageKind }) {
  return (
    <div className={`status-line ${messageKind === 'error' ? 'error' : ''}`}>
      <span className="status-mode">{mode}</span>
      {commandMode ? (
        <form className="status-command" onSubmit={onCommandSubmit}>
          <span>:</span>
          <input
            ref={commandInputRef}
            className="status-command-input"
            value={commandText}
            onChange={(event) => setCommandText(event.target.value)}
            autoComplete="off"
            spellCheck="false"
          />
        </form>
      ) : (
        <div className="status-message">
          <button className="status-command-trigger" onClick={onCommandOpen}>:</button>
          <span>{message}</span>
        </div>
      )}
    </div>
  );
}

function Topology({ lab, status, selected, setSelected, moveNode, connectNodes }) {
  const canvasRef = useRef(null);
  const [connecting, setConnecting] = useState(null);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const nodes = lab.layout?.nodes || {};
  const vms = lab.vms || [];
  const switches = lab.switches || [];
  const externalLinks = lab.externalLinks || [];
  const lines = [];
  for (const vm of vms) {
    for (const nic of vm.networks || []) {
      const target = nic.switch
        ? { type: 'switch', id: nic.switch }
        : nic.externalLink
          ? { type: 'external', id: nic.externalLink }
          : null;
      if (!target) continue;
      const a = nodes[vm.id] || { x: 80, y: 80 };
      const b = nodes[target.id] || defaultPoint(target.type);
      lines.push({ key: `${vm.id}-${target.type}-${target.id}`, d: orthogonalPath(a, b) });
    }
  }
  for (const sw of switches) {
    if (!sw.externalLink) continue;
    const a = nodes[sw.id] || defaultPoint('switch');
    const b = nodes[sw.externalLink] || defaultPoint('external');
    lines.push({ key: `${sw.id}-external-${sw.externalLink}`, d: orthogonalPath(a, b) });
  }
  if (connecting) {
    const from = nodes[connecting.from.id] || defaultPoint(connecting.from.type);
    lines.push({ key: 'draft-link', d: orthogonalPathToPoint(from, connecting.cursor), draft: true });
  }

  function canvasPoint(event) {
    const rect = canvasRef.current?.getBoundingClientRect();
    if (!rect) return { x: 0, y: 0 };
    return { x: event.clientX - rect.left - pan.x, y: event.clientY - rect.top - pan.y };
  }

  function snapCanvasPoint(event) {
    const point = canvasPoint(event);
    return snapPoint(point.x, point.y);
  }

  function parseNodeTarget(target) {
    const node = target?.closest?.('[data-node]');
    if (!node?.dataset.node) return null;
    const [type, id] = node.dataset.node.split(':');
    return type && id ? { type, id } : null;
  }

  function sameNode(a, b) {
    return a && b && a.type === b.type && a.id === b.id;
  }

  function nodeTargetAtPoint(clientX, clientY, from) {
    const direct = parseNodeTarget(document.elementFromPoint(clientX, clientY));
    if (direct && !sameNode(direct, from)) return direct;
    const tolerance = gridSize / 2;
    const nodeElements = Array.from(canvasRef.current?.querySelectorAll('[data-node]') || []).reverse();
    for (const node of nodeElements) {
      const target = parseNodeTarget(node);
      if (!target || sameNode(target, from)) continue;
      const rect = node.getBoundingClientRect();
      if (
        clientX >= rect.left - tolerance &&
        clientX <= rect.right + tolerance &&
        clientY >= rect.top - tolerance &&
        clientY <= rect.bottom + tolerance
      ) {
        return target;
      }
    }
    return null;
  }

  function startConnect(event, from) {
    event.preventDefault();
    event.stopPropagation();
    setSelected(from);
    setConnecting({ from, cursor: snapCanvasPoint(event) });
    function onMove(move) {
      setConnecting((current) => (current ? { ...current, cursor: snapCanvasPoint(move) } : current));
    }
    function onUp(up) {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      const to = nodeTargetAtPoint(up.clientX, up.clientY, from);
      if (to && !sameEndpoint(to, from)) {
        connectNodes(from, to);
      }
      setConnecting(null);
    }
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  }

  function startPan(event) {
    if (event.button !== 0 || event.target !== canvasRef.current) return;
    event.preventDefault();
    setSelected(null);
    const startX = event.clientX;
    const startY = event.clientY;
    const origin = { ...pan };
    canvasRef.current?.setPointerCapture?.(event.pointerId);
    function onMove(move) {
      setPan({ x: origin.x + move.clientX - startX, y: origin.y + move.clientY - startY });
    }
    function onUp() {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    }
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  }

  return (
    <div
      className="canvas"
      ref={canvasRef}
      onPointerDown={startPan}
      style={{
        backgroundPosition: `${pan.x}px ${pan.y}px, ${pan.x}px ${pan.y}px, ${pan.x}px ${pan.y}px, ${pan.x}px ${pan.y}px`,
      }}
    >
      <div className="topology-plane" style={{ transform: `translate(${pan.x}px, ${pan.y}px)` }}>
        <svg className="links">
          {lines.map((line) => (
            <path key={line.key} d={line.d} className={line.draft ? 'draft' : ''} />
          ))}
        </svg>
        {vms.map((vm) => (
          <Node
            key={vm.id}
            id={vm.id}
            label={vm.name || vm.id}
            type="vm"
            state={resourceState(status, 'vm', vm.id)}
            point={nodes[vm.id] || { x: 80, y: 80 }}
            active={selected?.type === 'vm' && selected?.id === vm.id}
            setSelected={setSelected}
            moveNode={moveNode}
            onConnectStart={startConnect}
          />
        ))}
        {switches.map((sw) => (
          <Node
            key={sw.id}
            id={sw.id}
            label={sw.name || sw.id}
            type="switch"
            state={resourceState(status, 'switch', sw.id)}
            point={nodes[sw.id] || { x: 320, y: 160 }}
            active={selected?.type === 'switch' && selected?.id === sw.id}
            setSelected={setSelected}
            moveNode={moveNode}
            onConnectStart={startConnect}
          />
        ))}
        {externalLinks.map((link) => (
          <Node
            key={link.id}
            id={link.id}
            label={link.name || link.interface || link.id}
            type="external"
            state={resourceState(status, 'external', link.id)}
            point={nodes[link.id] || { x: 560, y: 160 }}
            active={selected?.type === 'external' && selected?.id === link.id}
            setSelected={setSelected}
            moveNode={moveNode}
            onConnectStart={startConnect}
          />
        ))}
      </div>
    </div>
  );
}

function Node({ id, label, type, state, point, active, setSelected, moveNode, onConnectStart }) {
  function startDrag(event) {
    event.preventDefault();
    setSelected({ type, id });
    const startX = event.clientX;
    const startY = event.clientY;
    const origin = { ...point };
    function onMove(move) {
      moveNode(id, origin.x + move.clientX - startX, origin.y + move.clientY - startY);
    }
    function onUp() {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    }
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  }
  return (
    <button
      className={`node ${type} ${active ? 'active' : ''}`}
      style={{ left: point.x, top: point.y }}
      data-node={`${type}:${id}`}
      onPointerDown={startDrag}
    >
      <span className="node-connect" onPointerDown={(event) => onConnectStart(event, { type, id })}>+</span>
      <span className="node-main">
        <span className="node-kind">{nodeKind(type)}</span>
        <span className="node-text">{label}</span>
      </span>
      <span className={`node-state state-${state}`}>{state}</span>
    </button>
  );
}

function Inspector({ lab, setLab, selected, object, status, isos, networkInterfaces }) {
  if (!object) {
    return (
      <div className="inspector-body empty">
        <strong>$ virsh state</strong>
        <StatusBoard status={status} />
      </div>
    );
  }
  if (selected.type === 'vm') {
    return (
      <div className="inspector-body form">
        <h2>{object.id}</h2>
        <Field label="Name" value={object.name || ''} onChange={(value) => updateVM(lab, setLab, object.id, { name: value })} />
        <Field label="Memory MB" type="number" value={object.memoryMB} onChange={(value) => updateVM(lab, setLab, object.id, { memoryMB: Number(value) })} />
        <Field label="CPUs" type="number" value={object.cpus} onChange={(value) => updateVM(lab, setLab, object.id, { cpus: Number(value) })} />
        <Field label="Disk" value={object.disk || ''} onChange={(value) => updateVM(lab, setLab, object.id, { disk: value })} />
        <ISOField value={object.iso || ''} isos={isos} onChange={(value) => updateVM(lab, setLab, object.id, { iso: value })} />
        <label className="check">
          <input className="check-input" type="checkbox" checked={!!object.vnc} onChange={(e) => updateVM(lab, setLab, object.id, { vnc: e.target.checked })} />
          <span className="check-mark" aria-hidden="true" />
          <span>VNC console</span>
        </label>
        <ConsoleInfo lab={lab} vm={object} />
      </div>
    );
  }
  if (selected.type === 'switch') {
    return (
      <div className="inspector-body form">
        <h2>{object.id}</h2>
        <Field label="Name" value={object.name || ''} onChange={(value) => updateSwitch(lab, setLab, object.id, { name: value })} />
        <SwitchModeField value={object.mode || 'bridge'} onChange={(value) => updateSwitchMode(lab, setLab, object.id, value)} />
        <SwitchLinkField
          value={object.externalLink || ''}
          links={lab.externalLinks || []}
          onChange={(value) => updateSwitchExternalLink(lab, setLab, object.id, value)}
        />
      </div>
    );
  }
  return (
    <div className="inspector-body form">
      <h2>{object.id}</h2>
      <Field label="Name" value={object.name || ''} onChange={(value) => updateExternalLink(lab, setLab, object.id, { name: value })} />
      <InterfaceField value={object.interface || ''} interfaces={networkInterfaces} onChange={(value) => updateExternalLink(lab, setLab, object.id, { interface: value })} />
    </div>
  );
}

function StatusBoard({ status }) {
  const normalized = normalizeStatus(status);
  if (normalized.vms.length === 0 && normalized.switches.length === 0 && normalized.externalLinks.length === 0) {
    return <p className="state-empty">no resources</p>;
  }
  return (
    <div className="state-board">
      <ResourceSection title="VMS" rows={normalized.vms} />
      <ResourceSection title="SWITCHES" rows={normalized.switches} />
      <ResourceSection title="LINKS" rows={normalized.externalLinks} />
    </div>
  );
}

function ResourceSection({ title, rows }) {
  return (
    <section className="state-section">
      <h3>{title}</h3>
      {rows.length === 0 ? (
        <p className="state-empty">empty</p>
      ) : (
        <div className="state-table">
          <span>ID</span>
          <span>STATE</span>
          <span>NAME</span>
          {rows.map((row) => (
            <React.Fragment key={`${title}-${row.id || row.name}`}>
              <span>{row.id || '-'}</span>
              <span className={`state-${row.state || 'unknown'}`}>{row.state || 'unknown'}</span>
              <span>{row.name || '-'}</span>
            </React.Fragment>
          ))}
        </div>
      )}
    </section>
  );
}

function ConsoleInfo({ lab, vm }) {
  const [info, setInfo] = useState(null);
  const [state, setState] = useState('idle');
  async function loadConsole() {
    setState('opening');
    setInfo(null);
    try {
      const data = await apiJSON(`/api/labs/${lab.id}/vms/${vm.id}/console/open`, {
        method: 'POST',
      });
      setInfo(data);
      setState('opened');
    } catch (err) {
      setInfo({ error: err.message || 'Console unavailable' });
      setState('error');
    }
  }
  return (
    <section className="console">
      <div className="console-actions">
        <button onClick={loadConsole} disabled={state === 'opening'}>./console</button>
      </div>
      {info?.error && <p className="message-error">{info.error}</p>}
      {state !== 'idle' && !info?.error && <p>console {state}</p>}
      {info?.url && <p>{info.name || 'VNC Viewer'} {info.state}</p>}
    </section>
  );
}

function Field({ label, value, onChange, type = 'text' }) {
  return (
    <label>
      {label}
      <input type={type} value={value} onChange={(e) => onChange(e.target.value)} />
    </label>
  );
}

function ISOField({ value, isos, onChange }) {
  return (
    <>
      <label>
        ISO
        <input value={value} onChange={(e) => onChange(e.target.value)} placeholder="/home/user/Downloads/alpine.iso" />
      </label>
      <label>
        Found ISO
        <select value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">none</option>
          {(isos || []).map((iso) => (
            <option key={iso.path} value={iso.path}>{iso.name}</option>
          ))}
        </select>
      </label>
    </>
  );
}

function InterfaceField({ value, interfaces, onChange }) {
  const items = Array.isArray(interfaces) ? interfaces : [];
  const hasValue = value && items.some((item) => item.name === value);
  return (
    <label>
      Interface
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">select</option>
        {value && !hasValue && <option value={value}>{value}</option>}
        {items.map((item) => (
          <option key={item.name} value={item.name}>{interfaceLabel(item)}</option>
        ))}
      </select>
    </label>
  );
}

function SwitchModeField({ value, onChange }) {
  return (
    <label>
      Mode
      <select value={value || 'bridge'} onChange={(e) => onChange(e.target.value)}>
        <option value="bridge">bridge</option>
        <option value="nat">nat</option>
        <option value="macnat-bridge">macnat-bridge</option>
      </select>
    </label>
  );
}

function SwitchLinkField({ value, links, onChange }) {
  const items = Array.isArray(links) ? links : [];
  const hasValue = value && items.some((item) => item.id === value);
  return (
    <label>
      Uplink
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">none</option>
        {value && !hasValue && <option value={value}>{value}</option>}
        {items.map((link) => (
          <option key={link.id} value={link.id}>{link.name || link.id}</option>
        ))}
      </select>
    </label>
  );
}

function interfaceLabel(item) {
  if (!item?.flags) return item?.name || '';
  return `${item.name} ${item.flags}`;
}

function updateVM(lab, setLab, id, patch) {
  setLab({ ...lab, vms: (lab.vms || []).map((vm) => (vm.id === id ? { ...vm, ...patch } : vm)) });
}

function updateSwitch(lab, setLab, id, patch) {
  setLab({ ...lab, switches: (lab.switches || []).map((sw) => (sw.id === id ? { ...sw, ...patch } : sw)) });
}

function updateSwitchMode(lab, setLab, id, mode) {
  setLab((current) => {
    const switches = (current.switches || []).map((sw) => {
      if (sw.id !== id) return sw;
      return { ...sw, mode };
    });
    return { ...current, switches };
  });
}

function updateSwitchExternalLink(lab, setLab, id, externalLink) {
  setLab((current) => {
    const switches = (current.switches || []).map((sw) => {
      if (sw.id !== id) return sw;
      return { ...sw, mode: sw.mode || 'bridge', externalLink };
    });
    let layout = removeSwitchExternalLinks(current.layout, id);
    if (externalLink) {
      layout = addLayoutLink(layout, { type: 'switch', id }, { type: 'external', id: externalLink });
    }
    return { ...current, switches, layout };
  });
}

function updateExternalLink(lab, setLab, id, patch) {
  setLab({ ...lab, externalLinks: (lab.externalLinks || []).map((link) => (link.id === id ? { ...link, ...patch } : link)) });
}

function ensureDiskForVM(lab, vm) {
  if (!vm.disk || (lab.disks || []).some((disk) => cleanPath(disk.path) === cleanPath(vm.disk))) {
    return lab;
  }
  return {
    ...lab,
    disks: [...(lab.disks || []), { id: vm.id, path: vm.disk, sizeGB: 30, format: 'qcow2' }],
  };
}

function putNode(layout = { nodes: {}, links: [] }, id, x, y) {
  return { ...layout, nodes: { ...(layout.nodes || {}), [id]: snapPoint(x, y) } };
}

function removeNodeFromLab(lab, node) {
  const next = normalizeLab(lab);
  if (!node?.id) return next;
  const layout = removeLayoutNode(next.layout, node.id);
  if (node.type === 'vm') {
    const removed = next.vms.find((vm) => vm.id === node.id);
    const vms = next.vms.filter((vm) => vm.id !== node.id);
    const disks = removed?.disk
      ? next.disks.filter((disk) => cleanPath(disk.path) !== cleanPath(removed.disk) || vms.some((vm) => cleanPath(vm.disk) === cleanPath(disk.path)))
      : next.disks;
    return { ...next, vms, disks, layout };
  }
  if (node.type === 'switch') {
    return {
      ...next,
      switches: next.switches.filter((sw) => sw.id !== node.id),
      vms: next.vms.map((vm) => ({
        ...vm,
        networks: (vm.networks || []).filter((nic) => nic.switch !== node.id),
      })),
      layout,
    };
  }
  if (node.type === 'external') {
    return {
      ...next,
      externalLinks: next.externalLinks.filter((link) => link.id !== node.id),
      switches: next.switches.map((sw) => (
        sw.externalLink === node.id ? { ...sw, mode: sw.mode || 'bridge', externalLink: '' } : sw
      )),
      vms: next.vms.map((vm) => ({
        ...vm,
        networks: (vm.networks || []).filter((nic) => nic.externalLink !== node.id),
      })),
      layout,
    };
  }
  return { ...next, layout };
}

function removeLayoutNode(layout = { nodes: {}, links: [] }, id) {
  const nodes = { ...(layout.nodes || {}) };
  delete nodes[id];
  const links = (layout.links || []).filter((link) => link.from?.id !== id && link.to?.id !== id);
  return { ...layout, nodes, links };
}

function sameEndpoint(a, b) {
  return Boolean(a && b && a.type === b.type && a.id === b.id);
}

function endpointKey(endpoint) {
  return `${endpoint?.type || ''}:${endpoint?.id || ''}`;
}

function endpointPairKey(a, b) {
  return [endpointKey(a), endpointKey(b)].sort().join('--');
}

function addLayoutLink(layout = { nodes: {}, links: [] }, from, to) {
  const links = Array.isArray(layout.links) ? layout.links : [];
  const key = endpointPairKey(from, to);
  if (links.some((link) => endpointPairKey(link.from, link.to) === key)) return layout;
  return {
    ...layout,
    links: [...links, { from: { type: from.type, id: from.id }, to: { type: to.type, id: to.id } }],
  };
}

function removeSwitchExternalLinks(layout = { nodes: {}, links: [] }, switchId) {
  return {
    ...layout,
    links: (layout.links || []).filter((link) => {
      const endpoints = [link.from, link.to];
      const hasSwitch = endpoints.some((endpoint) => endpoint?.type === 'switch' && endpoint.id === switchId);
      const hasExternal = endpoints.some((endpoint) => endpoint?.type === 'external');
      return !(hasSwitch && hasExternal);
    }),
  };
}

function nodePoint(endpoint, nodes) {
  if (!endpoint?.id) return null;
  return nodes?.[endpoint.id] || defaultPoint(endpoint.type);
}

function snapPoint(x, y) {
  return {
    x: Math.round(x / gridSize) * gridSize,
    y: Math.round(y / gridSize) * gridSize,
  };
}

function defaultPoint(type) {
  if (type === 'vm') return { x: 80, y: 80 };
  if (type === 'external') return { x: 560, y: 160 };
  return { x: 320, y: 160 };
}

function nodeKind(type) {
  if (type === 'vm') return 'VM';
  if (type === 'external') return 'IF';
  return 'SW';
}

function orthogonalPathToPoint(a, point) {
  const ac = { x: a.x + nodeWidth / 2, y: a.y + nodeHeight / 2 };
  const dx = point.x - ac.x;
  const dy = point.y - ac.y;

  if (Math.abs(dx) >= Math.abs(dy)) {
    const start = { x: dx >= 0 ? a.x + nodeWidth : a.x, y: ac.y };
    const midX = Math.round((start.x + point.x) / 2);
    return `M ${start.x} ${start.y} H ${midX} V ${point.y} H ${point.x}`;
  }

  const start = { x: ac.x, y: dy >= 0 ? a.y + nodeHeight : a.y };
  const midY = Math.round((start.y + point.y) / 2);
  return `M ${start.x} ${start.y} V ${midY} H ${point.x} V ${point.y}`;
}

function orthogonalPath(a, b) {
  const ac = { x: a.x + nodeWidth / 2, y: a.y + nodeHeight / 2 };
  const bc = { x: b.x + nodeWidth / 2, y: b.y + nodeHeight / 2 };
  const dx = bc.x - ac.x;
  const dy = bc.y - ac.y;

  if (Math.abs(dx) >= Math.abs(dy)) {
    const start = { x: dx >= 0 ? a.x + nodeWidth : a.x, y: ac.y };
    const end = { x: dx >= 0 ? b.x : b.x + nodeWidth, y: bc.y };
    const midX = Math.round((start.x + end.x) / 2);
    return `M ${start.x} ${start.y} H ${midX} V ${end.y} H ${end.x}`;
  }

  const start = { x: ac.x, y: dy >= 0 ? a.y + nodeHeight : a.y };
  const end = { x: bc.x, y: dy >= 0 ? b.y : b.y + nodeHeight };
  const midY = Math.round((start.y + end.y) / 2);
  return `M ${start.x} ${start.y} V ${midY} H ${end.x} V ${end.y}`;
}

function uniqueId(prefix, existing) {
  let i = existing.length + 1;
  while (existing.includes(`${prefix}${i}`)) i += 1;
  return `${prefix}${i}`;
}

function ensureLabID(lab, labs) {
  if (lab.id) return lab;
  const id = uniqueId('lab-', (labs || []).map((item) => item.id));
  return { ...lab, id, name: lab.name || id };
}

function cleanPath(path) {
  return String(path || '').replace(/\\/g, '/').replace(/\/+/g, '/').replace(/(^|\/)\.\//g, '$1');
}

function preferredISO(isos) {
  const items = Array.isArray(isos) ? isos : [];
  const alpine = items.find((iso) => iso.name?.toLowerCase().includes('alpine'));
  return alpine?.path || items[0]?.path || '';
}

function nextLabDraft(labs) {
  const existing = new Set((labs || []).map((item) => item.id));
  let i = 1;
  while (existing.has(`lab-${i}`)) i += 1;
  return normalizeLab({ id: `lab-${i}`, name: `Lab ${i}` });
}

function normalizeLab(data = {}) {
  return {
    ...blankLab,
    ...data,
    vms: Array.isArray(data.vms) ? data.vms : [],
    switches: Array.isArray(data.switches) ? data.switches : [],
    externalLinks: Array.isArray(data.externalLinks) ? data.externalLinks : [],
    disks: Array.isArray(data.disks) ? data.disks : [],
    layout: { nodes: data.layout?.nodes || {}, links: Array.isArray(data.layout?.links) ? data.layout.links : [] },
  };
}

function normalizeStatus(data) {
  return {
    vms: Array.isArray(data?.vms) ? data.vms : [],
    switches: Array.isArray(data?.switches) ? data.switches : [],
    externalLinks: Array.isArray(data?.externalLinks) ? data.externalLinks : [],
  };
}

function resourceState(status, type, id) {
  const normalized = normalizeStatus(status);
  const list = type === 'vm'
    ? normalized.vms
    : type === 'external'
      ? normalized.externalLinks
      : normalized.switches;
  return list.find((item) => item.id === id)?.state || 'planned';
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
    const err = new Error(data?.error || `${res.status} ${res.statusText}`);
    err.status = res.status;
    throw err;
  }
  return data;
}

createRoot(document.getElementById('root')).render(<App />);
