import { chmodSync, copyFileSync, mkdirSync, rmSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const appRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = resolve(appRoot, "../..");
const platformArgument = process.argv.find((value) => value.startsWith("--platform="))?.split("=")[1];
const architectureArgument = process.argv.find((value) => value.startsWith("--arch="))?.split("=")[1];
const requested = process.argv.includes("--all") ? "all" : platformArgument && architectureArgument ? "explicit" : "host";
const hostArch = process.arch === "x64" ? "amd64" : process.arch;
const hostOS = process.platform === "win32" ? "windows" : process.platform;
const requestedArch = architectureArgument === "x64" ? "amd64" : architectureArgument;
const requestedOS = platformArgument === "win32" ? "windows" : platformArgument;
const targets = [
  { goos: "darwin", goarch: "arm64", directory: "darwin-arm64" },
  { goos: "darwin", goarch: "amd64", directory: "darwin-x64" },
  { goos: "windows", goarch: "amd64", directory: "win32-x64" },
  { goos: "windows", goarch: "arm64", directory: "win32-arm64" },
  { goos: "linux", goarch: "amd64", directory: "linux-x64" },
  { goos: "linux", goarch: "arm64", directory: "linux-arm64" },
].filter((target) => requested === "all" || (requested === "explicit" ? target.goos === requestedOS && target.goarch === requestedArch : target.goos === hostOS && target.goarch === hostArch));

if (targets.length === 0) throw new Error(`unsupported desktop target ${process.platform}/${process.arch}`);
for (const target of targets) {
  const outputDirectory = resolve(appRoot, "resources/bin", target.directory);
  rmSync(outputDirectory, { recursive: true, force: true });
  mkdirSync(outputDirectory, { recursive: true });
  const executable = resolve(outputDirectory, target.goos === "windows" ? "cable-player.exe" : "cable-player");
  const result = spawnSync("go", ["build", "-trimpath", "-ldflags", "-s -w -X main.version=0.1.0", "-o", executable, "./cmd/cable-player"], {
    cwd: repoRoot,
    env: { ...process.env, GOOS: target.goos, GOARCH: target.goarch, CGO_ENABLED: "0" },
    stdio: "inherit",
  });
  if (result.status !== 0) process.exit(result.status ?? 1);
  if (target.goos !== "windows") chmodSync(executable, 0o755);
  if ((requested === "host" && target.goos === hostOS && target.goarch === hostArch) || requested === "explicit") {
    const current = resolve(appRoot, "resources/bin/current");
    rmSync(current, { recursive: true, force: true });
    mkdirSync(current, { recursive: true });
    const currentExecutable = resolve(current, target.goos === "windows" ? "cable-player.exe" : "cable-player");
    copyFileSync(executable, currentExecutable);
    if (target.goos !== "windows") chmodSync(currentExecutable, 0o755);
  }
  console.log(`${target.directory}: ${executable}`);
}
