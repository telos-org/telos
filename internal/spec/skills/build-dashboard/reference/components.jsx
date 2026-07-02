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
 *  - Layout gaps come from <Page> / <Grid>, never hand-set per section. The
 *    recurring regression is sibling cards rendered flush (gap:0) so the page
 *    reads as one solid slab; the primitives own the spacing so that can't
 *    happen. The only spacing scale is 4 / 8 / 12 / 16 / 24px.
 *  - Status reads as a dot + a sentence-case word (see StatusTile), never a
 *    giant lowercase colored monospace headline — that "status banner" look is
 *    a strong AI-generated tell. Monospace is for real IDs / values / numbers
 *    only; labels and prose stay sans and sentence case.
 *  - No Kubernetes internals. Health is a service-level verdict (is it
 *    serving?), not pods / restarts / images / nodes / raw cluster events.
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

/* ── Layout primitives ───────────────────────────────────────────────
   Spacing is the thing that silently regresses when the page is assembled by
   hand: sections end up flush (gap:0) and the whole dashboard reads as one
   solid block. Let a primitive own the gap instead of re-deriving it each
   time. Compose the entire dashboard inside one <Page>, and put tile rows in
   <Grid>. Then spacing is correct by construction, not by luck. */

/* Page — centered canvas with steady vertical rhythm between every section. */
export function Page({ children, maxWidth = 960 }) {
  return (
    <div style={{
      maxWidth, margin: "0 auto", padding: "24px 20px 40px",
      display: "flex", flexDirection: "column", gap: 24, minWidth: 0,
    }}>
      {children}
    </div>
  );
}

/* Grid — responsive row of equal tiles (status / metric cards). The grid owns
   the gap so tiles never sit flush, and auto-fit keeps them from cramming when
   the viewport is narrow. Wrap each child in <Card> for a bordered tile. */
export function Grid({ children, min = 200, gap = 16 }) {
  return (
    <div style={{
      display: "grid", gap,
      gridTemplateColumns: `repeat(auto-fit, minmax(${min}px, 1fr))`,
      minWidth: 0,
    }}>
      {children}
    </div>
  );
}

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

/* ── HealthRow — one service-level health check ────────
   Replaces the old pod grid on purpose: operators care whether the service is
   actually serving, not about pod names, restart counts, images, or nodes.
   Compute `ok` from a real probe server-side (a query, an HTTP check) and keep
   `detail` short and human ("Accepting connections", "HTTP 200 · 42ms"). */
export function HealthRow({ name, ok, detail }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "6px 0", minWidth: 0 }}>
      <StatusDot ready={ok} />
      <span style={{ ...sans, fontSize: "0.8125rem", color: "var(--foreground)", flex: 1, minWidth: 0, overflowWrap: "anywhere" }}>
        {name}
      </span>
      {detail && (
        <span style={{ ...sans, fontSize: "0.75rem", color: "var(--muted-foreground)", flexShrink: 0 }}>{detail}</span>
      )}
    </div>
  );
}

/* ── MetricCard — a single numeric metric with a label ────────
   For NUMBERS only (counts, rates, sizes): monospace + tabular-nums keeps
   columns of digits aligned. Don't pass a status word here — a big colored
   lowercase word reads as an AI-generated "stat banner"; use StatusTile for
   state. Leave `color` unset unless the number itself is a genuine alert. */
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

/* ── StatusTile — compact state summary for the top KPI row ────────
   State reads as a dot + a sentence-case word ("Healthy", "Degraded"), not a
   giant lowercase colored monospace headline. Color lives in the dot; the word
   stays in the foreground unless it's a real problem, so a wall of these reads
   calm rather than like a neon status board. */
export function StatusTile({ label, ok, status }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8, minWidth: 0 }}>
      <span style={{ ...sans, fontSize: "0.6875rem", color: "var(--muted-foreground)", textTransform: "uppercase", letterSpacing: "0.04em" }}>
        {label}
      </span>
      <span style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
        <StatusDot ready={ok} />
        <span style={{ ...sans, fontSize: "0.9375rem", fontWeight: 600, color: ok ? "var(--foreground)" : "var(--destructive)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {status}
        </span>
      </span>
    </div>
  );
}

/* ── EventRow — one application/service event line ────────
   For events the service itself exposes (audit entries, job outcomes, service
   log events). NOT for raw Kubernetes events (BackOff, FailedScheduling, …) —
   those are cluster internals we deliberately don't surface. A type of
   "Warning" or "error" tints the row so real problems stand out. */
export function EventRow({ event }) {
  const isWarning = event.type === "Warning" || event.type === "error";
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
