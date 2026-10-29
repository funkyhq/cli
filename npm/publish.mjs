// Builds the npm packages from GoReleaser's dist/ and publishes them: one
// package per platform holding its binary (@funky-dev/cli-darwin-arm64, …),
// then @funky-dev/cli, which depends on all of them optionally so npm only
// installs the one matching the machine.
//
//   node npm/publish.mjs            publish (the release workflow does this)
//   node npm/publish.mjs --dry-run  build and pack into dist/npm only
//
// Run after `goreleaser release` (or `--snapshot`) has filled dist/.

import { execFileSync } from "node:child_process";
import { chmodSync, cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const dryRun = process.argv.includes("--dry-run");
const root = join(import.meta.dirname, "..");
const dist = join(root, "dist");
const out = join(dist, "npm");

const readJSON = (path) => JSON.parse(readFileSync(path, "utf8"));
const writeJSON = (path, value) => writeFileSync(path, JSON.stringify(value, null, 2) + "\n");

// Go's names for platforms, as Node's process.platform / process.arch.
const OS = { darwin: "darwin", linux: "linux", windows: "win32" };
const CPU = { amd64: "x64", arm64: "arm64" };

const { version } = readJSON(join(dist, "metadata.json"));
const main = readJSON(join(import.meta.dirname, "cli", "package.json"));
const shared = {
  version,
  homepage: main.homepage,
  repository: main.repository,
};

rmSync(out, { recursive: true, force: true });
const dirs = [];

for (const a of readJSON(join(dist, "artifacts.json"))) {
  if (a.type !== "Binary") continue;
  const os = OS[a.goos];
  const cpu = CPU[a.goarch];
  if (!os || !cpu) throw new Error(`no npm platform for ${a.goos}/${a.goarch}`);
  const name = `${main.name}-${os}-${cpu}`;
  const dir = join(out, `cli-${os}-${cpu}`);
  const exe = os === "win32" ? "funky.exe" : "funky";
  mkdirSync(join(dir, "bin"), { recursive: true });
  cpSync(join(root, a.path), join(dir, "bin", exe));
  chmodSync(join(dir, "bin", exe), 0o755);
  writeJSON(join(dir, "package.json"), {
    name,
    ...shared,
    description: `The ${os}-${cpu} binary for ${main.name}`,
    os: [os],
    cpu: [cpu],
    files: ["bin"],
  });
  writeFileSync(join(dir, "README.md"), `The ${os}-${cpu} binary for [${main.name}](https://www.npmjs.com/package/${main.name}). Install that instead.\n`);
  dirs.push({ name, dir });
  main.optionalDependencies[name] = version;
}
if (dirs.length === 0) throw new Error("no binaries in dist/artifacts.json");

const mainDir = join(out, "cli");
cpSync(join(import.meta.dirname, "cli"), mainDir, { recursive: true });
cpSync(join(root, "README.md"), join(mainDir, "README.md"));
main.version = version;
main.optionalDependencies = Object.fromEntries(Object.entries(main.optionalDependencies).sort());
writeJSON(join(mainDir, "package.json"), main);
// Last, so the binaries it depends on are already published.
dirs.push({ name: main.name, dir: mainDir });

const npm = (args, cwd) => execFileSync("npm", args, { cwd, stdio: ["ignore", "pipe", "inherit"], encoding: "utf8" });

// Pre-releases (v0.2.0-rc.1) don't become what `npm i @funky-dev/cli` installs.
const tag = version.includes("-") ? "next" : "latest";

for (const { name, dir } of dirs) {
  if (dryRun) {
    npm(["pack", "--pack-destination", out], dir);
    console.log(`packed ${name}@${version}`);
    continue;
  }
  // A re-run after a partial publish skips what's already out.
  if (published(name)) {
    console.log(`${name}@${version} already published`);
    continue;
  }
  npm(["publish", "--access", "public", "--provenance", "--tag", tag], dir);
  console.log(`published ${name}@${version}`);
}

function published(name) {
  try {
    return npm(["view", `${name}@${version}`, "version"], root).trim() === version;
  } catch {
    return false; // E404: never published
  }
}
