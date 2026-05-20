---
name: verify-frontend
description: |
  Frontend verification using headless Chromium and Playwright.
  HTTP 200 does NOT mean the frontend works. You MUST use a real browser.
  Catches React hydration errors, broken imports, console errors,
  blank pages, and API integration failures that curl cannot detect.
metadata:
  category: verification
  author: telos
allowed-tools: Bash(chromium:*) Bash(python3:*) Bash(playwright:*)
---

# Frontend Verification

**CRITICAL: Never verify a frontend with curl alone.**

`curl` only checks if the server responds. A Next.js/React app can return HTTP 200
with a valid HTML shell while the client-side JavaScript crashes completely.
The page looks blank in a browser but curl says "200 OK".

You MUST use Playwright to render the page in a real browser and check for errors.

## Step 1: Always Check for JS Errors First

Before checking anything else, load the page and capture console errors:

```python
#!/usr/bin/env python3
"""Frontend smoke test — catches what curl cannot."""
import sys
from playwright.sync_api import sync_playwright

URL = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:3000"
errors = []
console_errors = []

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, args=["--no-sandbox"])
    page = browser.new_page()

    # Capture uncaught exceptions
    page.on("pageerror", lambda err: errors.append(str(err)))

    # Capture console errors
    page.on("console", lambda msg: console_errors.append(msg.text)
            if msg.type == "error" else None)

    page.goto(URL, wait_until="networkidle", timeout=30000)
    page.wait_for_timeout(3000)  # let React hydrate

    # Report
    if errors:
        print(f"FAIL: {len(errors)} uncaught JS error(s):")
        for e in errors: print(f"  {e[:300]}")
        sys.exit(1)

    if console_errors:
        # Filter noise (favicon 404s etc)
        real = [e for e in console_errors if "favicon" not in e.lower()]
        if real:
            print(f"FAIL: {len(real)} console error(s):")
            for e in real: print(f"  {e[:300]}")
            sys.exit(1)

    body = page.inner_text("body")
    if len(body.strip()) < 20:
        print(f"FAIL: Page is blank ({len(body)} chars)")
        sys.exit(1)

    page.screenshot(path="/workspace/output/screenshot.png")
    print(f"PASS: Page rendered, {len(body)} chars, no JS errors")
    browser.close()
```

Save this as a test file and run it. If it fails, the frontend is broken
regardless of what curl says.

## Step 2: Verify Each Page

```python
PAGES = [
    ("/", "home"),
    ("/organizations", "browse"),
    ("/events", "events"),
    ("/login", "login"),
]

for path, name in PAGES:
    errors.clear()
    console_errors.clear()
    page.goto(f"{URL}{path}", wait_until="networkidle", timeout=30000)
    page.wait_for_timeout(2000)

    if errors or console_errors:
        print(f"FAIL [{name}]: JS errors on {path}")
        for e in errors + console_errors:
            print(f"  {e[:200]}")
    else:
        page.screenshot(path=f"/workspace/output/screenshot-{name}.png")
        print(f"PASS [{name}]")
```

## Step 3: Verify Data Loaded from API

HTTP 200 + no JS errors still doesn't mean the app works if the API
connection is broken. Check that real data appears:

```python
# After navigating to a data page
page.goto(f"{URL}/organizations", wait_until="networkidle", timeout=30000)
page.wait_for_timeout(3000)

# Check that API data rendered (not just the loading skeleton)
body = page.inner_text("body")
# Look for evidence of real data
assert "organization" in body.lower() or "club" in body.lower(), \
    f"No data rendered. Body: {body[:200]}"

# Or check specific elements
items = page.locator("table tr, [class*='card'], [class*='Card']").count()
assert items > 0, "No data items rendered"
```

## Step 4: Screenshot as Evidence

Always take screenshots. They prove exactly what is broken.

```python
# Desktop
page.set_viewport_size({"width": 1280, "height": 720})
page.screenshot(path="/workspace/output/desktop.png", full_page=True)

# Mobile
page.set_viewport_size({"width": 375, "height": 667})
page.screenshot(path="/workspace/output/mobile.png", full_page=True)
```

## Common Frontend Failures

| Symptom | Cause | How to detect |
|---------|-------|---------------|
| Blank page, 200 OK | React crashes on hydration | `pageerror` event fires |
| "Cannot read properties of undefined" | Bad import or missing dependency | Console error |
| Page loads but no data | API_URL wrong or CORS blocked | Check network requests |
| Charts don't render | Wrong data format for chart library | Screenshot shows empty area |
| Redirect loop | Auth check with wrong API endpoint | Page never reaches networkidle |
| "ReactDOM is not defined" | Missing dependency in standalone build | Console error on load |

## Quick One-Liner

For a fast pass/fail without writing a script:

```bash
python3 -c "
from playwright.sync_api import sync_playwright
errs = []
with sync_playwright() as p:
    b = p.chromium.launch(headless=True, args=['--no-sandbox'])
    pg = b.new_page()
    pg.on('pageerror', lambda e: errs.append(str(e)))
    pg.goto('http://SERVICE.NAMESPACE.svc.cluster.local:PORT', wait_until='networkidle', timeout=30000)
    pg.wait_for_timeout(3000)
    b.close()
    if errs: print('FAIL:', errs); exit(1)
    print('PASS: no JS errors')
"
```

## Important Notes

- Always use `--no-sandbox` for Chromium (running in container)
- Always `wait_for_timeout(2000-3000)` after navigation for React hydration
- Service URLs use K8s DNS: `http://service.namespace.svc.cluster.local:port`
- Screenshots go to `/workspace/output/` as evidence
- The implementation agent should also use Playwright before declaring progress
