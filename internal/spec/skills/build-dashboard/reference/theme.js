/**
 * Theme sync for generated dashboards.
 *
 * The Telos console embeds the dashboard in an iframe and tells it which mode to
 * use, two ways:
 *   - initial paint: a `?theme=light|dark` query param on the dashboard URL
 *   - live toggle:   a `postMessage({ type: "telos:theme", theme })` when the
 *                    operator flips the console's light/dark switch
 *
 * Call initDashboardTheme() once at startup (e.g. top of src/main.jsx, before
 * rendering). It sets the initial `.dark` class and keeps it in sync after.
 */
export function initDashboardTheme() {
  const root = document.documentElement;
  const apply = (mode) => {
    root.classList.toggle("dark", mode === "dark");
  };

  // Initial mode: the console's ?theme= wins; otherwise follow the OS, default dark.
  const param = new URLSearchParams(window.location.search).get("theme");
  const initial =
    param === "light" || param === "dark"
      ? param
      : window.matchMedia?.("(prefers-color-scheme: light)").matches
        ? "light"
        : "dark";
  apply(initial);

  // Live updates when the operator toggles the console theme.
  window.addEventListener("message", (event) => {
    const data = event.data;
    if (
      data &&
      data.type === "telos:theme" &&
      (data.theme === "light" || data.theme === "dark")
    ) {
      apply(data.theme);
    }
  });
}
