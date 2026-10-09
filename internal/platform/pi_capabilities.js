import { writeSync } from "node:fs";
import { VERSION } from "@earendil-works/pi-coding-agent";

// Exit during extension initialization, before Pi can dispatch a prompt.
export default function () {
  const [major, minor, patch] = VERSION.split(".").map(Number);
  writeSync(1, JSON.stringify({
    connection_switching: typeof Bun !== "undefined" && major === 1 && (minor > 0 || (minor === 0 && patch >= 4)),
    executable: process.execPath,
    script: typeof Bun === "undefined" ? process.argv[1] : undefined,
  }) + "\n");
  process.exit(0);
}
