import * as React from "react";

export function SplashScreen({ active }: { active: boolean }) {
  const splashStyle = React.useMemo<React.CSSProperties>(() => {
    const maxViewport = Math.max(window.innerWidth, window.innerHeight);
    const mandalaSize = Math.min(420, Math.max(1, window.innerWidth * 0.7));
    return { "--splash-expand": String(Math.max(1, (maxViewport / mandalaSize) * 1.1)) } as React.CSSProperties;
  }, []);
  return <div className={`splash-screen ${active ? "is-active" : ""}`} style={splashStyle} aria-hidden={!active}>
    <div className="splash-scanlines" aria-hidden="true" />
    <div className="splash-inner">
      <div className="splash-mandala" aria-hidden="true"><div className="splash-mandala-spin">
        {Array.from({ length: 12 }, (_, index) => <span key={index} className="splash-petal" style={{ "--i": index } as React.CSSProperties} />)}
        <div className="splash-burst">{Array.from({ length: 12 }, (_, index) => <span key={index} className="splash-burst-dot" style={{ "--i": index } as React.CSSProperties} />)}</div>
        <div className="splash-ring" /><div className="splash-core" />
      </div></div>
      <div className="splash-text"><div className="splash-title">Chiba Cable Television</div><div className="splash-subtitle">Signal Sync</div><div className="splash-tag">Initializing</div></div>
    </div>
  </div>;
}
