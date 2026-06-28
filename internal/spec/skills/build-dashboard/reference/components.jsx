/**
 * Telos dashboard component reference implementations.
 * These are patterns for the agent to adapt, not a runtime library.
 *
 * Styled to match the Telos console (@telos-org/design). Colors and fonts come
 * from CSS variables defined in theme.css (light + dark); these components
 * reference them via var(--token), so they follow the console's light/dark
 * toggle automatically — never hardcode a color here. IBM Plex Sans for UI text,
 * Geist Mono for values/IDs.
 *
 * Robustness rules baked in (do not regress these when adapting):
 *  - Every flex row that holds a long value sets `minWidth: 0`, and the value
 *    itself wraps (`overflowWrap: "anywhere"`) or truncates. This is the fix for
 *    text bursting out of its box — flex children will NOT shrink without it.
 *  - Copy uses the clipboard API with a synchronous textarea fallback, because
 *    `navigator.clipboard` is unavailable in insecure / sandboxed iframe
 *    contexts. Never let a copy button silently no-op.
 *  - Reveal is plain React state over the REAL value passed in as `value`.
 *    Pass the real secret here; the component masks it for display.
 */
import { useState } from "react";

const sans = { fontFamily: "var(--font-sans)" };
const mono = { fontFamily: "var(--font-mono)", fontVariantNumeric: "tabular-nums" };

/* ── copyText — clipboard with iframe/insecure-context fallback ──────── */
function copyText(value) {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(value).catch(() => fallbackCopy(value));
  }
  fallbackCopy(value);
  return Promise.resolve();
}
function fallbackCopy(value) {
  const ta = document.createElement("textarea");
  ta.value = value;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.top = "-9999px";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  try { document.execCommand("copy"); } catch { /* best effort */ }
  document.body.removeChild(ta);
}

/* Shared row: label + value, never overflows. */
const rowStyle = { display: "flex", alignItems: "center", gap: 10, padding: "5px 0", minWidth: 0 };
const labelStyle = { ...sans, fontSize: "0.8125rem", color: "var(--muted-foreground)", flexShrink: 0, minWidth: 104 };
const valueStyle = { ...mono, fontSize: "0.8125rem", flex: 1, minWidth: 0, overflowWrap: "anywhere" };

/* ── SecretValue — masked password/DSN with reveal + copy ──────── */
export function SecretValue({ value, label }) {
  const [shown, setShown] = useState(false);
  const [copied, setCopied] = useState(false);

  const copy = () => {
    copyText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div style={rowStyle}>
      <span style={labelStyle}>{label}</span>
      <code style={{ ...valueStyle, color: shown ? "var(--foreground)" : "var(--muted-foreground)" }}>
        {shown ? value : "••••••••••"}
      </code>
      <button onClick={() => setShown((s) => !s)} style={btnStyle}>{shown ? "Hide" : "Show"}</button>
      <button onClick={copy} style={{ ...btnStyle, ...(copied ? copiedBtn : {}) }}>
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

/* ── CopyValue — plain value with copy button ──────── */
export function CopyValue({ value, label }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    copyText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div style={rowStyle}>
      <span style={labelStyle}>{label}</span>
      <code style={{ ...valueStyle, color: "var(--foreground)" }}>{value}</code>
      <button onClick={copy} style={{ ...btnStyle, ...(copied ? copiedBtn : {}) }}>
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

/* ── StatusDot ──────── */
export function StatusDot({ ready, size = 8 }) {
  return (
    <span style={{
      width: size, height: size, borderRadius: "50%", display: "inline-block",
      background: ready ? "var(--success)" : "var(--destructive)", flexShrink: 0,
    }} />
  );
}

/* ── Card wrapper ──────── */
export function Card({ title, children }) {
  return (
    <div style={{
      background: "var(--card)", border: "1px solid var(--border)",
      borderRadius: "var(--radius)", padding: "1rem 1.25rem", minWidth: 0,
    }}>
      {title && (
        <div style={{ ...sans, fontSize: "0.875rem", fontWeight: 600, color: "var(--foreground)", marginBottom: 12 }}>
          {title}
        </div>
      )}
      {children}
    </div>
  );
}

/* ── EmptyState — for sections with no data (don't render a blank box) ──────── */
export function EmptyState({ children = "No data available." }) {
  return (
    <div style={{ ...sans, fontSize: "0.8125rem", color: "var(--muted-foreground)", padding: "4px 0" }}>
      {children}
    </div>
  );
}

/* ── LoadingState — placeholder while the first fetch is in flight ──────── */
export function LoadingState({ children = "Loading…" }) {
  return (
    <div style={{ ...sans, fontSize: "0.8125rem", color: "var(--muted-foreground)", padding: "4px 0" }}>
      {children}
    </div>
  );
}

/* ── PodRow ──────── */
export function PodRow({ pod }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "4px 0", minWidth: 0 }}>
      <StatusDot ready={pod.ready === "True" || pod.ready === true} />
      <span style={{ ...mono, fontSize: "0.8125rem", color: "var(--foreground)", flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
        {pod.name}
      </span>
      <span style={{ ...sans, fontSize: "0.75rem", color: "var(--muted-foreground)", flexShrink: 0 }}>{pod.phase}</span>
      {pod.restarts > 0 && (
        <span style={{ ...mono, fontSize: "0.6875rem", color: "var(--warning)", flexShrink: 0 }}>{pod.restarts}× restarts</span>
      )}
    </div>
  );
}

/* ── MetricCard — big number with label ──────── */
export function MetricCard({ value, label, color }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4, minWidth: 0 }}>
      <span style={{ ...mono, fontSize: "1.25rem", fontWeight: 600, color: color || "var(--foreground)" }}>
        {value}
      </span>
      <span style={{ ...sans, fontSize: "0.6875rem", color: "var(--muted-foreground)", textTransform: "uppercase", letterSpacing: "0.04em" }}>
        {label}
      </span>
    </div>
  );
}

/* ── EventRow ──────── */
export function EventRow({ event }) {
  const isWarning = event.type === "Warning";
  return (
    <div style={{ display: "flex", alignItems: "baseline", gap: 10, padding: "4px 0", minWidth: 0, fontSize: "0.8125rem" }}>
      <span style={{ ...sans, fontWeight: 500, color: isWarning ? "var(--destructive)" : "var(--muted-foreground)", flexShrink: 0, minWidth: 64 }}>{event.reason}</span>
      <span style={{ ...sans, color: "var(--foreground)", flex: 1, minWidth: 0, overflowWrap: "anywhere" }}>{event.message}</span>
      <span style={{ ...mono, color: "var(--muted-foreground)", fontSize: "0.6875rem", flexShrink: 0 }}>{event.object}</span>
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

  return <button onClick={download} style={btnStyle}>Download {filename}</button>;
}

/* ── Shared button style — neutral ghost, matches console ──────── */
const btnStyle = {
  ...sans, fontSize: "0.75rem", color: "var(--muted-foreground)", background: "transparent",
  border: "1px solid var(--border)", borderRadius: 6, padding: "3px 9px",
  cursor: "pointer", flexShrink: 0, whiteSpace: "nowrap",
};
const copiedBtn = { color: "var(--success)", borderColor: "var(--success)" };
