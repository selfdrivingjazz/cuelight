import * as React from "react";
import type { DesktopPreferences, DesktopStatus, DisplayInfo, ManualJoinInput, NetworkStatus } from "../shared/contracts";

const EMPTY_STATUS: DesktopStatus = {
  preferences: { autoStart: false, fullScreen: true, presentationMode: "automatic" },
  networks: [],
  displays: [],
  update: { appVersion: __CUELIGHT_VERSION__, state: "idle" },
  defaultNodeName: "",
};

type Perform = (label: string, operation: () => Promise<DesktopStatus | void>) => Promise<void>;

export function App() {
  const [status, setStatus] = React.useState<DesktopStatus>(EMPTY_STATUS);
  const [busy, setBusy] = React.useState<string>();
  const [error, setError] = React.useState<string>();

  React.useEffect(() => {
    void window.cuelightNode.status().then(setStatus).catch((value: unknown) => setError(message(value)));
    return window.cuelightNode.onStatus(setStatus);
  }, []);

  const perform: Perform = async (label, operation) => {
    setBusy(label);
    setError(undefined);
    try {
      const next = await operation();
      if (next) setStatus(next);
    } catch (value) { setError(message(value)); }
    finally { setBusy(undefined); }
  };

  const onAir = status.networks.filter((network) => network.onAir);
  const updatePreferences = (patch: Partial<DesktopPreferences>) => void perform("Updating", () => window.cuelightNode.updatePreferences({ ...status.preferences, ...patch }));

  return <main className="app-shell">
    <header className="masthead"><div><span className="signal" />CUELIGHT</div><span className="masthead-meta"><UpdateChip status={status} /><span className="phase">{onAir.length ? `${onAir.length} on air` : `${status.networks.length} network${status.networks.length === 1 ? "" : "s"}`}</span></span></header>
    <section className="hero">
      <p className="eyebrow">DESKTOP NODE</p>
      <h1>{status.networks.length ? `This computer is on ${status.networks.length} Cuelight network${status.networks.length === 1 ? "" : "s"}` : "Join a Cuelight network"}</h1>
      <p>Open a join link from a controller's Pair page, or paste it below. Each network gets its own identity and player; when several share a display, the most recent assignment goes on air unless you pin one.</p>
    </section>

    {error && <section className="error" role="alert"><strong>Operation failed</strong><span>{error}</span></section>}
    {status.pendingJoin && <PendingJoinCard status={status} busy={busy} perform={perform} />}

    <div className="workspace">
      <section className="panel">
        <div className="panel-heading"><div><p className="eyebrow">MEMBERSHIPS</p><h2>Networks</h2></div><span>{status.displays.length} display{status.displays.length === 1 ? "" : "s"}</span></div>
        {status.networks.length === 0 ? <p className="empty">No networks yet. Create a join link on a controller's <strong>Pair a screen</strong> page.</p>
          : <ul className="networks">{status.networks.map((network) => <NetworkCard key={network.id} network={network} displays={status.displays} busy={busy} perform={perform} />)}</ul>}
      </section>

      <section className="panel operations">
        <div className="panel-heading"><div><p className="eyebrow">ADD</p><h2>Join a network</h2></div></div>
        <PasteLink busy={busy} perform={perform} />
        <ManualJoin status={status} busy={busy} perform={perform} />
        <div className="settings">
          <p className="eyebrow">PREFERENCES</p>
          <label className="check"><input type="checkbox" checked={status.preferences.fullScreen} onChange={(event) => updatePreferences({ fullScreen: event.currentTarget.checked })} />Full-screen output</label>
          <label className="check"><input type="checkbox" checked={status.preferences.autoStart} onChange={(event) => updatePreferences({ autoStart: event.currentTarget.checked })} />Open at login and start every network</label>
          <label>Presentation mode<select value={status.preferences.presentationMode} onChange={(event) => updatePreferences({ presentationMode: event.currentTarget.value as DesktopPreferences["presentationMode"] })}><option value="automatic">Automatic</option><option value="media">Media</option><option value="media-schedule">Media schedule</option><option value="web">Web</option></select></label>
          <div className="actions"><button disabled={onAir.length === 0} onClick={() => void perform("Showing", () => window.cuelightNode.showOutput())}>Show output</button></div>
        </div>
      </section>
    </div>

    <footer><span>Cuelight {status.update.appVersion} · Esc on an output returns here</span><span>{status.update.detail ?? ""}</span></footer>
  </main>;
}

function UpdateChip({ status }: { status: DesktopStatus }) {
  const update = status.update;
  if (update.state === "idle") return null;
  const label = update.state === "staged" ? `update ${update.version} ready` : update.state === "error" ? "update failed" : `update ${update.version ?? ""} ${update.state}${update.percent === undefined ? "" : ` ${update.percent}%`}`;
  return <span className={`phase ${update.state === "error" ? "phase-error" : "phase-running"}`} title={update.detail}>{label}</span>;
}

function PendingJoinCard({ status, busy, perform }: { status: DesktopStatus; busy?: string; perform: Perform }) {
  const pending = status.pendingJoin!;
  const [nodeName, setNodeName] = React.useState(status.defaultNodeName);
  return <section className="join-card" aria-label="Join network">
    <div>
      <p className="eyebrow">JOIN LINK</p>
      <h2>Join “{pending.name}”?</h2>
      <p className="muted">{pending.controlUrl} · {pending.caPinned ? "self-managed certificate, pinned by the link" : "publicly trusted certificate"}</p>
      <p className="muted">That network's operators will be able to show content on this computer's displays.</p>
    </div>
    <form className="join-form" onSubmit={(event) => { event.preventDefault(); void perform("Joining", () => window.cuelightNode.join({ link: pending.link, nodeName })); }}>
      <label>This computer's name on that network<input required maxLength={120} value={nodeName} onChange={(event) => setNodeName(event.currentTarget.value)} /></label>
      <div className="actions"><button type="button" disabled={Boolean(busy)} onClick={() => void perform("Dismissing", () => window.cuelightNode.dismissJoin())}>Dismiss</button><button className="primary" disabled={Boolean(busy) || !nodeName.trim()}>{busy === "Joining" ? "Joining…" : "Join network"}</button></div>
    </form>
  </section>;
}

function PasteLink({ busy, perform }: { busy?: string; perform: Perform }) {
  const [link, setLink] = React.useState("");
  return <form className="form single" onSubmit={(event) => { event.preventDefault(); void perform("Reading link", async () => { const next = await window.cuelightNode.previewJoin(link.trim()); setLink(""); return next; }); }}>
    <label>Join link<input required autoComplete="off" spellCheck={false} placeholder="cuelight://join?v=1&url=…" value={link} onChange={(event) => setLink(event.currentTarget.value)} /></label>
    <button className="primary" disabled={Boolean(busy) || !link.trim()}>Continue</button>
  </form>;
}

function ManualJoin({ status, busy, perform }: { status: DesktopStatus; busy?: string; perform: Perform }) {
  const [form, setForm] = React.useState<ManualJoinInput>({ controlUrl: "", token: "", caSha256: "", networkName: "", nodeName: "", recover: true });
  const update = (patch: Partial<ManualJoinInput>) => setForm((current) => ({ ...current, ...patch }));
  return <details className="advanced">
    <summary>Advanced: recover or enroll with a token</summary>
    <form className="form" onSubmit={(event) => {
      event.preventDefault();
      void perform(form.recover ? "Recovering" : "Enrolling", async () => {
        const next = await window.cuelightNode.joinManual({ ...form, nodeName: form.nodeName || status.defaultNodeName, caSha256: form.caSha256 || undefined, networkName: form.networkName || undefined });
        update({ token: "" });
        return next;
      });
    }}>
      <label className="span-two">Control URL<input required type="url" value={form.controlUrl} placeholder="https://control.example" onChange={(event) => update({ controlUrl: event.currentTarget.value })} /></label>
      <label className="span-two">CA SHA-256 fingerprint (blank if publicly trusted)<input value={form.caSha256} spellCheck={false} placeholder="AB:CD:… or hex" onChange={(event) => update({ caSha256: event.currentTarget.value })} /></label>
      <label>Network name<input value={form.networkName} placeholder="defaults to the host" onChange={(event) => update({ networkName: event.currentTarget.value })} /></label>
      <label>Node name<input value={form.nodeName} placeholder={status.defaultNodeName} onChange={(event) => update({ nodeName: event.currentTarget.value })} /></label>
      <label className="span-two">One-time {form.recover ? "recovery" : "enrollment"} token<input required type="password" autoComplete="off" value={form.token} onChange={(event) => update({ token: event.currentTarget.value })} /></label>
      <label className="check span-two"><input type="checkbox" checked={form.recover} onChange={(event) => update({ recover: event.currentTarget.checked })} />Recover an existing node identity</label>
      <button disabled={Boolean(busy)}>{form.recover ? "Recover node" : "Enroll node"}</button>
    </form>
  </details>;
}

function NetworkCard({ network, displays, busy, perform }: { network: NetworkStatus; displays: DisplayInfo[]; busy?: string; perform: Perform }) {
  const [renaming, setRenaming] = React.useState<string>();
  const [confirmRemove, setConfirmRemove] = React.useState(false);
  const active = network.phase === "running" || network.phase === "starting";
  const state = network.onAir ? "on air" : network.standby ? "standby" : network.phase === "running" ? "idle" : network.phase;
  return <li className={`network network-${state.replace(" ", "-")}`}>
    <div className="network-head">
      {renaming === undefined
        ? <h3>{network.name}<button className="link" type="button" onClick={() => setRenaming(network.name)}>Rename</button></h3>
        : <form className="rename" onSubmit={(event) => { event.preventDefault(); void perform("Renaming", async () => { const next = await window.cuelightNode.rename(network.id, renaming); setRenaming(undefined); return next; }); }}>
          <input autoFocus required maxLength={120} value={renaming} onChange={(event) => setRenaming(event.currentTarget.value)} /><button className="primary">Save</button><button type="button" onClick={() => setRenaming(undefined)}>Cancel</button>
        </form>}
      <span className={`phase state-${state.replace(" ", "-")}`}>{state}</span>
    </div>
    <dl className="facts compact">
      <div><dt>Control</dt><dd>{network.controlUrl}</dd></div>
      <div><dt>Node</dt><dd>{network.node ? `${network.node.nodeName} · ${network.node.nodeId.slice(0, 8)}` : "missing identity"}</dd></div>
    </dl>
    <div className="network-controls">
      <label>Display<select value={String(network.displayBinding)} onChange={(event) => void perform("Updating", () => window.cuelightNode.setDisplay(network.id, event.currentTarget.value === "shared" ? "shared" : Number(event.currentTarget.value)))}>
        <option value="shared">Shared (primary display)</option>
        {displays.map((display) => <option key={display.id} value={String(display.id)}>{display.label} · {display.width}×{display.height}{display.primary ? " · primary" : ""}</option>)}
        {network.displayBinding !== "shared" && !displays.some((display) => display.id === network.displayBinding) && <option value={String(network.displayBinding)}>Disconnected display (using primary)</option>}
      </select></label>
      <label className="check"><input type="checkbox" checked={network.pinned} onChange={(event) => void perform("Updating", () => window.cuelightNode.setPinned(network.id, event.currentTarget.checked))} />Pin on air</label>
    </div>
    <div className="actions">
      {active ? <button disabled={Boolean(busy)} onClick={() => void perform("Stopping", () => window.cuelightNode.stop(network.id))}>Stop</button>
        : <button className="primary" disabled={Boolean(busy) || !network.node} onClick={() => void perform("Starting", () => window.cuelightNode.start(network.id))}>Start</button>}
      <button onClick={() => void perform("Opening logs", () => window.cuelightNode.openLogs(network.id))}>Logs</button>
      {confirmRemove
        ? <><button onClick={() => setConfirmRemove(false)}>Keep</button><button className="danger" disabled={Boolean(busy)} onClick={() => void perform("Removing", () => window.cuelightNode.remove(network.id))}>Remove and delete identity</button></>
        : <button onClick={() => setConfirmRemove(true)}>Remove…</button>}
    </div>
    {confirmRemove && <p className="runtime-error">Removing stops this network's player and deletes its identity, cache, and logs. Rejoining needs a new join link.</p>}
    {network.lastError && <p className="runtime-error">{network.lastError}</p>}
  </li>;
}

function message(value: unknown): string {
  const text = value instanceof Error ? value.message : typeof value === "string" ? value : "Unknown desktop node error";
  // ipcRenderer.invoke prefixes main-process errors with the channel name.
  return text.replace(/^Error invoking remote method '[^']+': (Error: )?/, "");
}
