#!/usr/bin/env node
"use strict";

// Runs the funky binary from the platform build npm installed next to this
// package: @funky-dev/cli lists every platform's build (published as its
// own version, e.g. 1.2.3-darwin-arm64) as an optional dependency named
// @funky-dev/cli-<platform>, and npm only installs the one matching this
// OS and CPU.

const { spawnSync } = require("node:child_process");

const pkg = `@funky-dev/cli-${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "funky.exe" : "funky";

let bin;
try {
  bin = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  console.error(
    `funky: ${pkg} is not installed. Either this platform isn't supported, or\n` +
      "optional dependencies were skipped (--omit=optional, --no-optional):\n" +
      "reinstall @funky-dev/cli without that flag. Binaries for every platform\n" +
      "are also at https://github.com/funkyhq/cli/releases/latest",
  );
  process.exit(1);
}

const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(`funky: ${result.error.message}`);
  process.exit(1);
}
if (result.signal) {
  process.kill(process.pid, result.signal);
}
process.exit(result.status ?? 1);
