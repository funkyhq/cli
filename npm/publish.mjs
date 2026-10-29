// Builds the npm packages from GoReleaser's dist/ and publishes them, all
// as one npm package, the way @openai/codex does:
//
//   @funky-dev/cli@1.2.3-darwin-arm64, … one version per platform, holding
//                                        that platform's binary
//   @funky-dev/cli@1.2.3                 the shim, which lists them all as
//                                        optional dependencies under alias
//                                        names (@funky-dev/cli-darwin-arm64:
//                                        npm:@funky-dev/cli@1.2.3-darwin-arm64)
//
// npm only installs the alias whose os/cpu match the machine, and
// bin/funky.js runs the binary from it.
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
  name: main.name,
  license: main.license,
  homepage: main.homepage,
  repository: main.repository,
};

// Every version carries the README and LICENSE, so the npm page reads the
// same whichever version it shows.
const addDocs = (dir) => {
  cpSync(join(root, "README.md"), join(dir, "README.md"));
  cpSync(join(root, "LICENSE"), join(dir, "LICENSE"));
};

rmSync(out, { recursive: true, force: true });
const builds = [];

for (const a of readJSON(join(dist, "artifacts.json"))) {
  if (a.type !== "Binary") continue;
  const os = OS[a.goos];
  const cpu = CPU[a.goarch];
  if (!os || !cpu) throw new Error(`no npm platform for ${a.goos}/${a.goarch}`);
  const platform = `${os}-${cpu}`;
  const dir = join(out, platform);
  const exe = os === "win32" ? "funky.exe" : "funky";
  mkdirSync(join(dir, "bin"), { recursive: true });
  cpSync(join(root, a.path), join(dir, "bin", exe));
  chmodSync(join(dir, "bin", exe), 0o755);
  addDocs(dir);
  writeJSON(join(dir, "package.json"), {
    ...shared,
    version: `${version}-${platform}`,
    description: `The ${platform} binary for ${main.name}; install ${main.name} instead`,
    os: [os],
    cpu: [cpu],
    files: ["bin"],
  });
  // A version needs a dist-tag; one per platform keeps them off `latest`.
  builds.push({ version: `${version}-${platform}`, dir, tag: platform });
  main.optionalDependencies[`${main.name}-${platform}`] = `npm:${main.name}@${version}-${platform}`;
}
if (builds.length === 0) throw new Error("no binaries in dist/artifacts.json");

const mainDir = join(out, "cli");
cpSync(join(import.meta.dirname, "cli"), mainDir, { recursive: true });
addDocs(mainDir);
main.version = version;
main.optionalDependencies = Object.fromEntries(Object.entries(main.optionalDependencies).sort());
writeJSON(join(mainDir, "package.json"), main);
// Last, so the versions it depends on are already published. Pre-releases
// (v0.2.0-rc.1) don't become what `npm i @funky-dev/cli` installs.
builds.push({ version, dir: mainDir, tag: version.includes("-") ? "next" : "latest" });

const npm = (args, cwd, stderr = "inherit") =>
  execFileSync("npm", args, { cwd, stdio: ["ignore", "pipe", stderr], encoding: "utf8" });

for (const { version, dir, tag } of builds) {
  const spec = `${main.name}@${version}`;
  if (dryRun) {
    npm(["pack", "--pack-destination", out], dir);
    console.log(`packed ${spec}`);
    continue;
  }
  // A re-run after a partial publish skips what's already out.
  if (published(spec)) {
    console.log(`${spec} already published`);
    continue;
  }
  npm(["publish", "--access", "public", "--provenance", "--tag", tag], dir);
  console.log(`published ${spec}`);
}

function published(spec) {
  try {
    // stderr ignored: a 404 here just means "not yet".
    return npm(["view", spec, "version"], root, "ignore").trim() !== "";
  } catch {
    return false;
  }
}
