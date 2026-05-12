/**
 * Telos dashboard component reference implementations.
 * These are patterns for the agent to adapt, not a runtime library.
 */
import { useState } from "react";

/* ── Tokens (inline for self-containment) ──────────── */
const T = {
  void: "#0a1220", abyss: "#0f1829", ink: "#162236", deep: "#1e2e46",
  structure: "#283c58", mid: "#4d6580", muted: "#7e93aa", soft: "#a8bace",
  light: "#dae2ed", primary: "#3b82f6", signal: "#4ade80", warm: "#60a5fa",
  danger: "#f87171",
};
const mono = { fontFamily: '"JetBrains Mono", monospace', letterSpacing: "-0.02em" };

/* ── SecretValue — masked password/DSN with reveal + copy ──────── */
export function SecretValue({ value, label }) {
  const [shown, setShown] = useState(false);
  const [copied, setCopied] = useState(false);

  const copy = () => {
    navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <div style={{ display: "flex", alignItems: "baseline", gap: 8, padding: "3px 0" }}>
      <span style={{ ...mono, fontSize: "0.6rem", color: T.muted, minWidth: 44 }}>{label}</span>
      <code style={{ ...mono, fontSize: "0.7rem", color: shown ? T.light : T.mid, flex: 1, wordBreak: "break-all" }}>
        {shown ? value : "••••••••••"}
      </code>
      <button onClick={() => setShown(!shown)} style={btnStyle}>{shown ? "hide" : "show"}</button>
      <button onClick={copy} style={{ ...btnStyle, ...(copied ? { color: T.signal } : {}) }}>
        {copied ? "copied" : "copy"}
      </button>
    </div>
  );
}

/* ── CopyValue — plain value with copy button ──────── */
export function CopyValue({ value, label }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <div style={{ display: "flex", alignItems: "baseline", gap: 8, padding: "3px 0" }}>
      <span style={{ ...mono, fontSize: "0.6rem", color: T.muted, minWidth: 44 }}>{label}</span>
      <code style={{ ...mono, fontSize: "0.7rem", color: T.light, flex: 1, wordBreak: "break-all" }}>{value}</code>
      <button onClick={copy} style={{ ...btnStyle, ...(copied ? { color: T.signal } : {}) }}>
        {copied ? "copied" : "copy"}
      </button>
    </div>
  );
}

/* ── StatusDot ──────── */
export function StatusDot({ ready, size = 7 }) {
  return (
    <span style={{
      width: size, height: size, borderRadius: "50%", display: "inline-block",
      background: ready ? T.signal : T.danger, flexShrink: 0,
    }} />
  );
}

/* ── Card wrapper ──────── */
export function Card({ title, children }) {
  return (
    <div style={{
      background: `${T.structure}1a`, border: `1px solid ${T.structure}30`,
      borderRadius: 4, padding: "10px 12px",
    }}>
      {title && (
        <div style={{ ...mono, fontSize: "0.58rem", color: T.mid, textTransform: "uppercase", letterSpacing: "0.06em", marginBottom: 8 }}>
          {title}
        </div>
      )}
      {children}
    </div>
  );
}

/* ── PodRow ──────── */
export function PodRow({ pod }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "2px 0" }}>
      <StatusDot ready={pod.ready === "True" || pod.ready === true} />
      <span style={{ ...mono, fontSize: "0.68rem", color: T.light, flex: 1 }}>{pod.name}</span>
      <span style={{ ...mono, fontSize: "0.58rem", color: T.mid }}>{pod.phase}</span>
      {pod.restarts > 0 && (
        <span style={{ ...mono, fontSize: "0.54rem", color: T.warm }}>{pod.restarts}x</span>
      )}
    </div>
  );
}

/* ── MetricCard — big number with label ──────── */
export function MetricCard({ value, label, color }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      <span style={{ ...mono, fontSize: "1.1rem", fontWeight: 600, color: color || T.light, fontVariantNumeric: "tabular-nums" }}>
        {value}
      </span>
      <span style={{ ...mono, fontSize: "0.5rem", color: T.mid, textTransform: "uppercase", letterSpacing: "0.04em" }}>
        {label}
      </span>
    </div>
  );
}

/* ── EventRow ──────── */
export function EventRow({ event }) {
  const isWarning = event.type === "Warning";
  return (
    <div style={{ display: "flex", alignItems: "baseline", gap: 8, padding: "3px 0", fontSize: "0.62rem" }}>
      <span style={{ ...mono, color: isWarning ? T.danger : T.mid, minWidth: 50 }}>{event.reason}</span>
      <span style={{ color: T.soft, flex: 1 }}>{event.message}</span>
      <span style={{ ...mono, color: T.mid, fontSize: "0.54rem", flexShrink: 0 }}>{event.object}</span>
    </div>
  );
}

/* ── CertDownload ──────── */
export function CertDownload({ pem, filename = "ca.crt" }) {
  const download = () => {
    const blob = new Blob([pem], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a"); a.href = url; a.download = filename; a.click();
    URL.revokeObjectURL(url);
  };

  return <button onClick={download} style={{ ...btnStyle, color: T.signal }}>download {filename}</button>;
}

/* ── Shared button style ──────── */
const btnStyle = {
  ...mono, fontSize: "0.5rem", color: T.primary, background: "transparent",
  border: `1px solid ${T.primary}33`, borderRadius: 2, padding: "1px 5px",
  cursor: "pointer", flexShrink: 0,
};
