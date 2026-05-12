#!/usr/bin/env python3
"""Take a headless screenshot of a URL.

Usage:
    python3 screenshot.py <url> [output.png] [--width 1280] [--height 720] [--full-page] [--wait N]

Uses Playwright with system Chromium. No display server required.
"""

import argparse
import sys

from playwright.sync_api import sync_playwright


def screenshot(
    url: str,
    output: str = "screenshot.png",
    width: int = 1280,
    height: int = 720,
    full_page: bool = False,
    wait_seconds: int = 2,
) -> str:
    with sync_playwright() as p:
        browser = p.chromium.launch(
            headless=True,
            args=["--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage"],
        )
        page = browser.new_page(viewport={"width": width, "height": height})

        # Collect console errors
        errors: list[str] = []
        page.on(
            "console",
            lambda msg: errors.append(msg.text) if msg.type == "error" else None,
        )

        page.goto(url, wait_until="networkidle", timeout=30_000)

        if wait_seconds > 0:
            page.wait_for_timeout(wait_seconds * 1000)

        page.screenshot(path=output, full_page=full_page)
        title = page.title()
        browser.close()

    print(f"Screenshot saved: {output}")
    print(f"Page title: {title}")
    if errors:
        print(f"Console errors ({len(errors)}):")
        for err in errors:
            print(f"  - {err}")
    return output


def main() -> None:
    parser = argparse.ArgumentParser(description="Headless screenshot utility")
    parser.add_argument("url", help="URL to screenshot")
    parser.add_argument(
        "output", nargs="?", default="screenshot.png", help="Output path"
    )
    parser.add_argument("--width", type=int, default=1280)
    parser.add_argument("--height", type=int, default=720)
    parser.add_argument("--full-page", action="store_true")
    parser.add_argument(
        "--wait", type=int, default=2, help="Seconds to wait after load"
    )
    args = parser.parse_args()

    try:
        screenshot(
            args.url, args.output, args.width, args.height, args.full_page, args.wait
        )
    except Exception as e:
        print(f"Screenshot failed: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
