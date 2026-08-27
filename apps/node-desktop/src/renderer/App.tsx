import * as React from "react";
import type { DesktopStatus, ProvisionInput } from "../shared/contracts";

const EMPTY_STATUS: DesktopStatus = { phase: "unprovisioned", outputWindows: 0, settings: { autoStart: false, fullScreen: true }, logPath: "" };

export function App() {
  const [status, setStatus] = React.useState<DesktopStatus>(EMPTY_STATUS);
  const [busy, setBusy] = React.useState<string>();
  const [error, setError] = React.useState<string>();
  const [form, setForm] = React.useState<ProvisionInput>({ controllerUrl: "", caPath: "", token: "", nodeName: `${navigator.platform} Cable Node`, recover: false });

  React.useEffect(() => {
    void window.cableNode.status().then(setStatus).catch((value: unknown) => setError(message(value)));
    return window.cableNode.onStatus(setStatus);
  }, []);

  const perform = async (label: string, operation: () => Promise<DesktopStatus>) => {
    setBusy(label);
    setError(undefined);
    try { setStatus(await operation()); }
    catch (value) { setError(message(value)); }
    finally { setBusy(undefined); }
  };

  const provision = (event: React.FormEvent) => {
    event.preventDefault();
    void perform(form.recover ? "Recovering" : "Enrolling", async () => {
      const next = await window.cableNode.provision(form);
      setForm((value) => ({ ...value, token: "" }));
      return next;
    });
  };

  const updateForm = (field: keyof ProvisionInput, value: string | boolean) => setForm((current) => ({ ...current, [field]: value }));
  const active = status.phase === "running" || status.phase === "starting";

  return <main className="app-shell">
    <header className="masthead"><div><span className="signal" />CABLE NODE</div><span className={`phase phase-${status.phase}`}>{status.phase}</span></header>
    <section className="hero">
      <p className="eyebrow">DESKTOP CAPABILITY HOST</p>
      <h1>{status.node?.nodeName || "Make this computer a Cable node"}</h1>
      <p>Enroll over your tailnet, then use the controller on desktop to assign signed media and Cable releases. Electron's bundled Chromium is the output renderer.</p>
    </section>

    {error && <section className="error" role="alert"><strong>Node operation failed</strong><span>{error}</span></section>}

    <div className="workspace">
      <section className="panel">
        <div className="panel-heading"><div><p className="eyebrow">IDENTITY</p><h2>{status.node ? "Provisioned node" : "Enroll this node"}</h2></div><span>{status.node?.nodeId ? status.node.nodeId.slice(0, 8) : "UNBOUND"}</span></div>
        {status.node ? <dl className="facts">
          <div><dt>Node ID</dt><dd>{status.node.nodeId}</dd></div>
          <div><dt>Controller</dt><dd>{status.node.controlUrl}</dd></div>
          <div><dt>Event epoch</dt><dd>{status.node.eventEpoch}</dd></div>
          <div><dt>Publisher</dt><dd>{status.node.publisherKeyId}</dd></div>
        </dl> : <form className="form" onSubmit={provision}>
          <label>Controller URL<input required type="url" value={form.controllerUrl} placeholder="https://desktop.tailnet.example/api/v1" onChange={(event) => updateForm("controllerUrl", event.currentTarget.value)} /></label>
          <label>Node name<input required value={form.nodeName} onChange={(event) => updateForm("nodeName", event.currentTarget.value)} /></label>
          <label className="span-two">Controller CA<div className="field-action"><input required value={form.caPath} placeholder="Choose the controller CA PEM" onChange={(event) => updateForm("caPath", event.currentTarget.value)} /><button type="button" onClick={() => void window.cableNode.chooseCA().then((value) => value && updateForm("caPath", value))}>Choose</button></div></label>
          <label className="span-two">One-time {form.recover ? "recovery" : "enrollment"} token<input required type="password" autoComplete="off" value={form.token} onChange={(event) => updateForm("token", event.currentTarget.value)} /></label>
          <label className="check span-two"><input type="checkbox" checked={form.recover} onChange={(event) => updateForm("recover", event.currentTarget.checked)} />Recover an existing node identity</label>
          <button className="primary" disabled={Boolean(busy)}>{busy ?? (form.recover ? "Recover node" : "Enroll node")}</button>
        </form>}
      </section>

      <section className="panel operations">
        <div className="panel-heading"><div><p className="eyebrow">RUNTIME</p><h2>Player process</h2></div><span>{status.playerPid ? `PID ${status.playerPid}` : "OFFLINE"}</span></div>
        <div className="runtime-mark"><span className={active ? "pulse" : "idle"} /><strong>{active ? "Ready for assignments" : "Player stopped"}</strong><small>{status.outputWindows} output window{status.outputWindows === 1 ? "" : "s"}</small></div>
        <div className="actions">
          <button className="primary" disabled={!status.node || active || Boolean(busy)} onClick={() => void perform("Starting", () => window.cableNode.start())}>{status.phase === "starting" ? "Starting…" : "Start node"}</button>
          <button disabled={!active || Boolean(busy)} onClick={() => void perform("Stopping", () => window.cableNode.stop())}>Stop node</button>
          <button onClick={() => void window.cableNode.openLogs()}>Show logs</button>
        </div>
        <div className="settings">
          <label className="check"><input type="checkbox" checked={status.settings.fullScreen} onChange={(event) => void perform("Updating", () => window.cableNode.updateSettings({ ...status.settings, fullScreen: event.currentTarget.checked }))} />Full-screen output</label>
          <label className="check"><input type="checkbox" checked={status.settings.autoStart} onChange={(event) => void perform("Updating", () => window.cableNode.updateSettings({ ...status.settings, autoStart: event.currentTarget.checked }))} />Start node at login</label>
        </div>
        {status.lastError && <p className="runtime-error">{status.lastError}</p>}
      </section>
    </div>

    <section className="proof-strip"><span>1 · Start control on desktop</span><span>2 · Tunnel HTTPS over Tailscale/MTS</span><span>3 · Enroll this machine</span><span>4 · Assign media</span></section>
    <footer><span>Bundled Chromium · signed releases · local CAS · LKG</span><span>{status.logPath}</span></footer>
  </main>;
}

function message(value: unknown): string {
  return value instanceof Error ? value.message : typeof value === "string" ? value : "Unknown desktop node error";
}
