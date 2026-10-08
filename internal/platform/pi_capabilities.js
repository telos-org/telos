import { writeSync } from "node:fs";
import { VERSION } from "@earendil-works/pi-coding-agent";

// Exit during extension initialization, before Pi can dispatch a prompt.
export default function () {
  const [major, minor, patch] = VERSION.split(".").map(Number);
  if (typeof Bun === "undefined" || major !== 1 || !(minor > 0 || (minor === 0 && patch >= 4))) {
    process.exit(78);
  }
  writeSync(1, "TELOS_PI_CONNECTION_SWITCHING\n");
  process.exit(0);
}
