import * as React from "react";
import type { Channel, Occurrence, Presentation } from "../presentation";

interface LegacyGuideProps {
  presentation: Presentation;
  selectedChannelId: string | null;
  selectedOccurrence?: Occurrence;
  offline: boolean;
  onSelectChannel(channel: Channel): void;
  onTune(occurrence: Occurrence): void;
}

const time = (value: string, timezone: string) => new Intl.DateTimeFormat("en-US", { timeZone: timezone, hour: "numeric", minute: "2-digit" }).format(new Date(value));

export function LegacyGuide({ presentation, selectedChannelId, selectedOccurrence, offline, onSelectChannel, onTune }: LegacyGuideProps) {
  const [now, setNow] = React.useState(() => new Date());
  React.useEffect(() => { const timer = window.setInterval(() => setNow(new Date()), 1_000); return () => window.clearInterval(timer); }, []);
  const channels = presentation.channels.filter((channel) => channel.enabled);
  const selectedChannel = channels.find((channel) => channel.id === selectedChannelId) ?? channels[0];
  const selected = selectedOccurrence ?? presentation.occurrences.find((occurrence) => occurrence.channelId === selectedChannel?.id) ?? presentation.occurrences[0];
  const starts = [...new Set(presentation.occurrences.map((occurrence) => occurrence.startsAt))].sort().slice(0, 6);
  const progress = selected ? Math.max(0, Math.min(100, ((now.getTime() - new Date(selected.startsAt).getTime()) / (new Date(selected.endsAt).getTime() - new Date(selected.startsAt).getTime())) * 100)) : 0;
  const move = (delta: number) => {
    if (!selectedChannel || channels.length === 0) return;
    const index = channels.findIndex((channel) => channel.id === selectedChannel.id);
    const next = channels[(index + delta + channels.length) % channels.length];
    if (next) onSelectChannel(next);
  };
  return <main className="guide-shell" style={{ "--slots": Math.max(1, starts.length), "--row-gap": "8px" } as React.CSSProperties} tabIndex={0} onKeyDown={(event) => {
    if (event.target !== event.currentTarget) return;
    if (event.key === "ArrowUp") { event.preventDefault(); move(-1); }
    if (event.key === "ArrowDown") { event.preventDefault(); move(1); }
    if (event.key === "Enter" && selected) { event.preventDefault(); onTune(selected); }
  }}>
    <div className="guide-noise" aria-hidden="true" />
    <header className="guide-header">
      <div className="header-card">
        <div className="poster">
          <div className="poster-preview">{selected?.media.kind === "video" ? <video key={selected.id} className="poster-preview-media" src={selected.media.reference} autoPlay muted loop playsInline /> : selected ? <img className="poster-preview-media" src={selected.media.reference} alt="" /> : null}</div>
          {!selected && <div className="poster-glow" />}
          {!selected && <div className="poster-label">{selectedChannel?.callsign ?? "CABLE"}</div>}
        </div>
        <div className="header-content"><span className="header-eyebrow">Guide</span><h1 className="header-title">{selected?.title ?? "Live schedule"}</h1><p className="header-subtitle">{selected?.subtitle ?? "Cable television"}</p><div className="header-meta"><span>{selectedChannel?.name ?? "Network"}</span><span>—</span><span>{selected?.media.kind.toUpperCase() ?? "STANDBY"}</span>{offline && <span className="offline-label">OFFLINE REVISION</span>}</div><div className="header-progress"><div className="header-progress-bar" style={{ width: `${progress}%` }} /></div></div>
      </div>
      <div className="header-right"><div className="header-clock"><span className="clock-time">{now.toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" })}</span><span className="clock-date">{now.toLocaleDateString("en-US", { weekday: "short", month: "short", day: "numeric" })}</span></div><div className="header-badges"><span className="badge live">Live</span><span className="badge">Rev {presentation.revision.id.slice(0, 8)}</span></div><div className="header-hint">↑ ↓ tune · Enter opens programme</div></div>
    </header>
    <section className="guide-grid" aria-label="Cable programme guide">
      <div className="time-row"><div className="time-label"><div className="time-now">{now.toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" })}</div><div className="time-label-text">Network</div></div><div className="time-slots">{starts.map((start, index) => <div key={start} className={`time-slot ${selected?.startsAt === start ? "is-active" : ""}`}>{time(start, presentation.revision.timezone)}</div>)}</div></div>
      <div className="channel-viewport"><div className="channel-rows">{channels.map((channel) => {
        const active = channel.id === selectedChannel?.id;
        const occurrences = presentation.occurrences.filter((occurrence) => occurrence.channelId === channel.id).sort((left, right) => left.startsAt.localeCompare(right.startsAt));
        return <div key={channel.id} className={`channel-row ${active ? "is-active" : ""}`}>
          <button className="channel-cell" type="button" style={{ borderColor: channel.accent }} onClick={() => onSelectChannel(channel)}><span className="channel-number">{String(channel.number).padStart(2, "0")}</span><span className="channel-name">{channel.name}</span><span className="channel-call">{channel.callsign}</span></button>
          <div className="program-grid">{occurrences.map((occurrence) => {
            const column = Math.max(0, starts.indexOf(occurrence.startsAt));
            const isActive = occurrence.id === selected?.id;
            return <button key={occurrence.id} className={`program-card ${isActive ? "is-active" : ""}`} type="button" style={{ gridColumn: `${column + 1} / span 1`, borderColor: isActive ? channel.accent : undefined }} onClick={() => { onSelectChannel(channel); onTune(occurrence); }}><span className="program-tag">{time(occurrence.startsAt, presentation.revision.timezone)}</span><span className="program-title">{occurrence.title}</span><span className="program-subtitle">{occurrence.subtitle ?? "Programme"}</span></button>;
          })}{occurrences.length === 0 && <div className="program-card standby"><span className="program-tag">Standby</span><span className="program-title">No verified programme</span></div>}</div>
        </div>;
      })}</div></div>
    </section>
    <footer className="guide-footer"><div className="ticker"><span>Chiba Cable Television — channel {selectedChannel?.number ?? "—"} — {selected?.title ?? "Signal standby"} — compiled revision {presentation.revision.id}</span></div></footer>
  </main>;
}
