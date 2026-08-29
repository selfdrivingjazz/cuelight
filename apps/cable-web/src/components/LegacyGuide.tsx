import * as React from "react";
import type { Block, Channel, Occurrence, Presentation } from "../presentation";
import { RemoteQRCode } from "./RemoteInput";

interface LegacyGuideProps {
  presentation: Presentation;
  selectedChannelId: string | null;
  offline: boolean;
  remoteURL?: string;
  showQR?: boolean;
  onSelectChannel(channel: Channel): void;
  onTune(occurrence: Occurrence): void;
}

const time = (value: string | Date, timezone: string) => new Intl.DateTimeFormat("en-US", { timeZone: timezone, hour: "numeric", minute: "2-digit" }).format(new Date(value));
const date = (value: Date, timezone: string) => new Intl.DateTimeFormat("en-US", { timeZone: timezone, weekday: "short", month: "short", day: "numeric" }).format(value);
const visibleHours = 3;
const slotMinutesForDensity = (density?: string) => density === "quarter-hour" ? 15 : density === "hour" ? 60 : 30;

function inferredBlocks(presentation: Presentation): Block[] {
  if (presentation.blocks) return presentation.blocks;
  const grouped = new Map<string, Block>();
  for (const occurrence of presentation.occurrences) {
    const id = occurrence.blockId ?? occurrence.id;
    const existing = grouped.get(id);
    if (!existing) {
      grouped.set(id, { id, channelId: occurrence.channelId, startsAt: occurrence.startsAt, endsAt: occurrence.endsAt, title: occurrence.title, source: "asset" });
      continue;
    }
    if (occurrence.startsAt < existing.startsAt) existing.startsAt = occurrence.startsAt;
    if (occurrence.endsAt > existing.endsAt) existing.endsAt = occurrence.endsAt;
  }
  return [...grouped.values()];
}

export function LegacyGuide({ presentation, selectedChannelId, offline, remoteURL, showQR, onSelectChannel, onTune }: LegacyGuideProps) {
  const [now, setNow] = React.useState(() => new Date());
  React.useEffect(() => { const timer = window.setInterval(() => setNow(new Date()), 1_000); return () => window.clearInterval(timer); }, []);
  const channels = presentation.channels.filter((channel) => channel.enabled);
  const selectedChannel = channels.find((channel) => channel.id === selectedChannelId) ?? channels[0];
  const nowMs = now.getTime();
  const selected = presentation.occurrences
    .filter((occurrence) => occurrence.channelId === selectedChannel?.id)
    .find((occurrence) => new Date(occurrence.startsAt).getTime() <= nowMs && nowMs < new Date(occurrence.endsAt).getTime());
  const slotMinutes = slotMinutesForDensity(presentation.revision.guideDensity);
  const slotDurationMs = slotMinutes * 60_000;
  const slotCount = Math.max(1, Math.round((visibleHours * 60) / slotMinutes));
  const visibleStartMs = Math.floor(nowMs / slotDurationMs) * slotDurationMs;
  const visibleEndMs = visibleStartMs + slotCount * slotDurationMs;
  const slots = Array.from({ length: slotCount }, (_, index) => new Date(visibleStartMs + index * slotDurationMs));
  const blocks = React.useMemo(() => inferredBlocks(presentation), [presentation]);
  const progress = selected ? Math.max(0, Math.min(100, ((nowMs - new Date(selected.startsAt).getTime()) / (new Date(selected.endsAt).getTime() - new Date(selected.startsAt).getTime())) * 100)) : 0;
  const move = (delta: number) => {
    if (!selectedChannel || channels.length === 0) return;
    const index = channels.findIndex((channel) => channel.id === selectedChannel.id);
    const next = channels[(index + delta + channels.length) % channels.length];
    if (next) onSelectChannel(next);
  };
  return <main className="guide-shell" style={{ "--slots": slotCount, "--row-gap": "8px" } as React.CSSProperties} tabIndex={0} onKeyDown={(event) => {
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
        <div className="header-content"><h1 className="header-title">{selected?.title ?? "Off air"}</h1><p className="header-subtitle">{selected?.subtitle ?? selectedChannel?.callsign ?? "Cable Television"}</p><div className="header-meta"><span>{selectedChannel?.name ?? "Network"}</span>{selected && <><span>—</span><span>{time(selected.startsAt, presentation.revision.timezone)}–{time(selected.endsAt, presentation.revision.timezone)}</span></>}{offline && <span className="offline-label">Schedule may be out of date</span>}</div><div className="header-progress"><div className="header-progress-bar" style={{ width: `${progress}%` }} /></div></div>
      </div>
      <div className="header-right"><div className="header-clock"><span className="clock-time">{time(now, presentation.revision.timezone)}</span><span className="clock-date">{date(now, presentation.revision.timezone)}</span></div></div>
    </header>
    <section className="guide-grid" aria-label="Cable programme guide">
      <div className="time-row"><div className="time-label"><div className="time-now">{time(now, presentation.revision.timezone)}</div><div className="time-label-text">Channels</div></div><div className="time-slots">{slots.map((slot, index) => <div key={slot.toISOString()} className={`time-slot ${index === 0 ? "is-active" : ""}`}>{time(slot, presentation.revision.timezone)}</div>)}</div></div>
      <div className="channel-viewport"><div className="channel-rows">{channels.map((channel) => {
        const active = channel.id === selectedChannel?.id;
        const scheduled = blocks
          .filter((block) => block.channelId === channel.id && new Date(block.endsAt).getTime() > visibleStartMs && new Date(block.startsAt).getTime() < visibleEndMs)
          .sort((left, right) => left.startsAt.localeCompare(right.startsAt));
        return <div key={channel.id} className={`channel-row ${active ? "is-active" : ""}`}>
          <button className="channel-cell" type="button" aria-pressed={active} style={{ borderColor: channel.accent }} onClick={() => onSelectChannel(channel)}><span className="channel-number">{String(channel.number).padStart(2, "0")}</span><span className="channel-name">{channel.name}</span><span className="channel-call">{channel.callsign}</span></button>
          <div className="program-grid">{scheduled.map((block) => {
            const startsAt = new Date(block.startsAt).getTime();
            const endsAt = new Date(block.endsAt).getTime();
            const clippedStart = Math.max(startsAt, visibleStartMs);
            const clippedEnd = Math.min(endsAt, visibleEndMs);
            const left = ((clippedStart - visibleStartMs) / (visibleEndMs - visibleStartMs)) * 100;
            const width = ((clippedEnd - clippedStart) / (visibleEndMs - visibleStartMs)) * 100;
            const isActive = selected?.blockId === block.id;
            const currentItem = presentation.occurrences.find((occurrence) => occurrence.blockId === block.id && new Date(occurrence.startsAt).getTime() <= nowMs && nowMs < new Date(occurrence.endsAt).getTime());
            return <button key={block.id} className={`program-card ${isActive ? "is-active" : ""}`} type="button" aria-current={isActive ? "true" : undefined} style={{ left: `${left}%`, width: `calc(${width}% - 8px)`, borderColor: isActive ? channel.accent : undefined }} onClick={() => onSelectChannel(channel)} onDoubleClick={() => { if (currentItem) onTune(currentItem); }}><span className="program-tag">{time(block.startsAt, presentation.revision.timezone)}–{time(block.endsAt, presentation.revision.timezone)}</span><span className="program-title">{block.title}</span><span className="program-subtitle">{currentItem?.title ?? (block.source === "playlist" ? "Playlist block" : "Media block")}</span></button>;
          })}{scheduled.length === 0 && <div className="program-card standby"><span className="program-title">Off air</span></div>}</div>
        </div>;
      })}</div></div>
    </section>
    {showQR && remoteURL && <RemoteQRCode url={remoteURL} />}
  </main>;
}
