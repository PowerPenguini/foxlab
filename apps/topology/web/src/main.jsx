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
const nodeHeight = 52;
const routeGap = 32;
const routeLaneGap = 16;

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
  const nodeEntries = [
    ...vms.map((vm) => ({ endpoint: { type: 'vm', id: vm.id }, point: nodes[vm.id] || { x: 80, y: 80 } })),
    ...switches.map((sw) => ({ endpoint: { type: 'switch', id: sw.id }, point: nodes[sw.id] || defaultPoint('switch') })),
    ...externalLinks.map((link) => ({ endpoint: { type: 'external', id: link.id }, point: nodes[link.id] || defaultPoint('external') })),
  ];
  const obstacles = nodeEntries.map((entry) => ({ ...nodeRect(entry.point), key: endpointKey(entry.endpoint) }));
  const edges = [];
  for (const vm of vms) {
    for (const nic of vm.networks || []) {
      const target = nic.switch
        ? { type: 'switch', id: nic.switch }
        : nic.externalLink
          ? { type: 'external', id: nic.externalLink }
          : null;
      if (!target) continue;
      edges.push({ key: `${vm.id}-${target.type}-${target.id}`, from: { type: 'vm', id: vm.id }, to: target });
    }
  }
  for (const sw of switches) {
    if (!sw.externalLink) continue;
    edges.push({ key: `${sw.id}-external-${sw.externalLink}`, from: { type: 'switch', id: sw.id }, to: { type: 'external', id: sw.externalLink } });
  }
  const lines = routeEdges(edges, nodes, obstacles);
  if (connecting) {
    const from = nodes[connecting.from.id] || defaultPoint(connecting.from.type);
    lines.push({ key: 'draft-link', d: routePathToPoint(connecting.from, from, connecting.cursor, obstacles), draft: true });
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

function routeEdges(edges, nodes, obstacles) {
  const plans = edges.map((edge) => {
    const fromPoint = nodePoint(edge.from, nodes) || defaultPoint(edge.from.type);
    const toPoint = nodePoint(edge.to, nodes) || defaultPoint(edge.to.type);
    const fromRect = nodeRect(fromPoint);
    const toRect = nodeRect(toPoint);
    const fromSide = sideToward(fromRect, toRect);
    const toSide = sideToward(toRect, fromRect);
    return { ...edge, fromPoint, toPoint, fromRect, toRect, fromSide, toSide };
  });
  const offsets = portOffsets(plans);
  const usedSegments = [];
  return plans.map((plan) => {
    const route = routeBetweenNodes(plan, obstacles, usedSegments, offsets);
    usedSegments.push(...route.segments);
    return { key: plan.key, d: route.d };
  });
}

function portOffsets(plans) {
  const groups = new Map();
  for (const plan of plans) {
    addPortPlan(groups, plan, 'from');
    addPortPlan(groups, plan, 'to');
  }
  const offsets = new Map();
  for (const [, items] of groups) {
    items.sort((a, b) => a.sort - b.sort || a.edge.key.localeCompare(b.edge.key));
    for (let i = 0; i < items.length; i++) {
      offsets.set(`${items[i].edge.key}:${items[i].end}`, sidePortOffset(items[i].side, i, items.length));
    }
  }
  return offsets;
}

function addPortPlan(groups, edge, end) {
  const endpoint = edge[end];
  const side = edge[`${end}Side`];
  const otherRect = edge[end === 'from' ? 'toRect' : 'fromRect'];
  const key = `${endpointKey(endpoint)}:${side}`;
  const sort = side === 'left' || side === 'right' ? otherRect.cy : otherRect.cx;
  if (!groups.has(key)) groups.set(key, []);
  groups.get(key).push({ edge, end, side, sort });
}

function sidePortOffset(side, index, count) {
  if (count <= 1) return 0;
  const span = side === 'top' || side === 'bottom'
    ? nodeWidth - routeGap * 2
    : Math.max(routeLaneGap * 2, nodeHeight - gridSize * 2);
  const maxSteps = Math.max(1, Math.floor(span / (gridSize * 2)));
  const lane = -maxSteps + (maxSteps * 2 * index) / (count - 1);
  return snapValue(lane * gridSize);
}

function routeBetweenNodes(plan, obstacles, usedSegments, offsets) {
  const fromOffset = offsets.get(`${plan.key}:from`) || 0;
  const toOffset = offsets.get(`${plan.key}:to`) || 0;
  const start = portPoint(plan.fromRect, plan.fromSide, fromOffset);
  const end = portPoint(plan.toRect, plan.toSide, toOffset);
  const startExit = moveOut(start, plan.fromSide, routeGap);
  const endExit = moveOut(end, plan.toSide, routeGap);
  const candidates = candidateRoutes(start, startExit, endExit, end, plan.fromRect, plan.toRect);
  return bestRoute(candidates, obstacles, usedSegments, [endpointKey(plan.from), endpointKey(plan.to)]);
}

function routePathToPoint(from, fromPoint, point, obstacles) {
  const fromRect = nodeRect(fromPoint);
  const pointRect = { left: point.x, right: point.x, top: point.y, bottom: point.y, cx: point.x, cy: point.y };
  const side = sideToward(fromRect, pointRect);
  const start = portPoint(fromRect, side, 0);
  const startExit = moveOut(start, side, routeGap);
  const candidates = [
    compactRoute([start, startExit, { x: point.x, y: startExit.y }, point]),
    compactRoute([start, startExit, { x: startExit.x, y: point.y }, point]),
  ];
  return bestRoute(candidates, obstacles, [], [endpointKey(from)]).d;
}

function candidateRoutes(start, startExit, endExit, end, fromRect, toRect) {
  const midX = snapValue((startExit.x + endExit.x) / 2);
  const midY = snapValue((startExit.y + endExit.y) / 2);
  const leftLane = snapValue(Math.min(fromRect.left, toRect.left) - routeGap);
  const rightLane = snapValue(Math.max(fromRect.right, toRect.right) + routeGap);
  const topLane = snapValue(Math.min(fromRect.top, toRect.top) - routeGap);
  const bottomLane = snapValue(Math.max(fromRect.bottom, toRect.bottom) + routeGap);

  return [
    compactRoute([start, startExit, { x: midX, y: startExit.y }, { x: midX, y: endExit.y }, endExit, end]),
    compactRoute([start, startExit, { x: startExit.x, y: midY }, { x: endExit.x, y: midY }, endExit, end]),
    compactRoute([start, startExit, { x: endExit.x, y: startExit.y }, endExit, end]),
    compactRoute([start, startExit, { x: startExit.x, y: endExit.y }, endExit, end]),
    compactRoute([start, startExit, { x: startExit.x, y: topLane }, { x: endExit.x, y: topLane }, endExit, end]),
    compactRoute([start, startExit, { x: startExit.x, y: bottomLane }, { x: endExit.x, y: bottomLane }, endExit, end]),
    compactRoute([start, startExit, { x: leftLane, y: startExit.y }, { x: leftLane, y: endExit.y }, endExit, end]),
    compactRoute([start, startExit, { x: rightLane, y: startExit.y }, { x: rightLane, y: endExit.y }, endExit, end]),
  ];
}

function bestRoute(candidates, obstacles, usedSegments, endpointKeys) {
  let best = null;
  for (const points of candidates) {
    const segments = pathSegments(points);
    const score = routeScore(points, segments, obstacles, usedSegments, endpointKeys);
    if (!best || score < best.score) {
      best = { d: pointsToPath(points), segments, score };
    }
  }
  return best || { d: '', segments: [] };
}

function routeScore(points, segments, obstacles, usedSegments, endpointKeys) {
  const endpointSet = new Set(endpointKeys);
  let score = pathLength(segments) + bendCount(points) * 12;
  for (const segment of segments) {
    for (const obstacle of obstacles) {
      if (endpointSet.has(obstacle.key)) continue;
      if (segmentIntersectsRect(segment, expandRect(obstacle, 8))) score += 10000;
      else if (segmentIntersectsRect(segment, expandRect(obstacle, 20))) score += 400;
    }
    for (const used of usedSegments) {
      if (segmentsOverlap(segment, used)) score += 300 + overlapLength(segment, used);
      else if (segmentsCross(segment, used)) score += 1500;
    }
  }
  return score;
}

function nodeRect(point) {
  return {
    left: point.x,
    right: point.x + nodeWidth,
    top: point.y,
    bottom: point.y + nodeHeight,
    cx: point.x + nodeWidth / 2,
    cy: point.y + nodeHeight / 2,
  };
}

function expandRect(rect, padding) {
  return {
    left: rect.left - padding,
    right: rect.right + padding,
    top: rect.top - padding,
    bottom: rect.bottom + padding,
  };
}

function sideToward(a, b) {
  const dx = b.cx - a.cx;
  const dy = b.cy - a.cy;
  if (Math.abs(dx) >= Math.abs(dy)) return dx >= 0 ? 'right' : 'left';
  return dy >= 0 ? 'bottom' : 'top';
}

function portPoint(rect, side, offset) {
  if (side === 'left') return { x: rect.left, y: snapValue(rect.cy + offset) };
  if (side === 'right') return { x: rect.right, y: snapValue(rect.cy + offset) };
  if (side === 'top') return { x: snapValue(rect.cx + offset), y: rect.top };
  return { x: snapValue(rect.cx + offset), y: rect.bottom };
}

function moveOut(point, side, distance) {
  if (side === 'left') return { x: point.x - distance, y: point.y };
  if (side === 'right') return { x: point.x + distance, y: point.y };
  if (side === 'top') return { x: point.x, y: point.y - distance };
  return { x: point.x, y: point.y + distance };
}

function compactRoute(points) {
  const deduped = [];
  for (const point of points) {
    const next = { x: snapValue(point.x), y: snapValue(point.y) };
    const last = deduped[deduped.length - 1];
    if (!last || last.x !== next.x || last.y !== next.y) deduped.push(next);
  }
  const compacted = [];
  for (const point of deduped) {
    compacted.push(point);
    while (compacted.length >= 3) {
      const a = compacted[compacted.length - 3];
      const b = compacted[compacted.length - 2];
      const c = compacted[compacted.length - 1];
      if ((a.x === b.x && b.x === c.x) || (a.y === b.y && b.y === c.y)) {
        compacted.splice(compacted.length - 2, 1);
      } else {
        break;
      }
    }
  }
  return compacted;
}

function pathSegments(points) {
  const segments = [];
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1];
    const b = points[i];
    if (a.x === b.x && a.y === b.y) continue;
    segments.push({
      x1: a.x,
      y1: a.y,
      x2: b.x,
      y2: b.y,
      vertical: a.x === b.x,
      horizontal: a.y === b.y,
    });
  }
  return segments;
}

function pointsToPath(points) {
  if (points.length === 0) return '';
  let d = `M ${points[0].x} ${points[0].y}`;
  for (let i = 1; i < points.length; i++) {
    const prev = points[i - 1];
    const point = points[i];
    if (point.x === prev.x) d += ` V ${point.y}`;
    else if (point.y === prev.y) d += ` H ${point.x}`;
    else d += ` L ${point.x} ${point.y}`;
  }
  return d;
}

function pathLength(segments) {
  return segments.reduce((sum, segment) => sum + Math.abs(segment.x2 - segment.x1) + Math.abs(segment.y2 - segment.y1), 0);
}

function bendCount(points) {
  let bends = 0;
  for (let i = 2; i < points.length; i++) {
    const a = points[i - 2];
    const b = points[i - 1];
    const c = points[i];
    if ((a.x === b.x) !== (b.x === c.x)) bends++;
  }
  return bends;
}

function segmentIntersectsRect(segment, rect) {
  if (segment.horizontal) {
    const minX = Math.min(segment.x1, segment.x2);
    const maxX = Math.max(segment.x1, segment.x2);
    return segment.y1 >= rect.top && segment.y1 <= rect.bottom && maxX >= rect.left && minX <= rect.right;
  }
  if (segment.vertical) {
    const minY = Math.min(segment.y1, segment.y2);
    const maxY = Math.max(segment.y1, segment.y2);
    return segment.x1 >= rect.left && segment.x1 <= rect.right && maxY >= rect.top && minY <= rect.bottom;
  }
  return false;
}

function segmentsOverlap(a, b) {
  if (a.horizontal && b.horizontal && a.y1 === b.y1) {
    return rangeOverlap(a.x1, a.x2, b.x1, b.x2) > 0;
  }
  if (a.vertical && b.vertical && a.x1 === b.x1) {
    return rangeOverlap(a.y1, a.y2, b.y1, b.y2) > 0;
  }
  return false;
}

function overlapLength(a, b) {
  if (a.horizontal && b.horizontal) return rangeOverlap(a.x1, a.x2, b.x1, b.x2);
  if (a.vertical && b.vertical) return rangeOverlap(a.y1, a.y2, b.y1, b.y2);
  return 0;
}

function segmentsCross(a, b) {
  const h = a.horizontal ? a : b.horizontal ? b : null;
  const v = a.vertical ? a : b.vertical ? b : null;
  if (!h || !v) return false;
  return betweenStrict(v.x1, h.x1, h.x2) && betweenStrict(h.y1, v.y1, v.y2);
}

function rangeOverlap(a1, a2, b1, b2) {
  const aMin = Math.min(a1, a2);
  const aMax = Math.max(a1, a2);
  const bMin = Math.min(b1, b2);
  const bMax = Math.max(b1, b2);
  return Math.max(0, Math.min(aMax, bMax) - Math.max(aMin, bMin));
}

function between(value, a, b) {
  return value >= Math.min(a, b) && value <= Math.max(a, b);
}

function betweenStrict(value, a, b) {
  return value > Math.min(a, b) && value < Math.max(a, b);
}

function snapValue(value) {
  return Math.round(value / gridSize) * gridSize;
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
