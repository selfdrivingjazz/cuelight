#!/usr/bin/env node
// Sets the release version everywhere it is recorded:
//
//   node scripts/set-version.mjs 0.3.0
//
// Writes VERSION and the "version" of every workspace package.json, leaving
// the rest of each file byte-for-byte intact.
import { readFileSync, writeFileSync } from "node:fs";
import { relative } from "node:path";
import { packageFiles, root, semver, versionFile } from "./versioning.mjs";

const version = process.argv[2];
if (process.argv.length !== 3 || !semver.test(version ?? "")) {
  console.error("usage: node scripts/set-version.mjs MAJOR.MINOR.PATCH[-PRERELEASE]");
  process.exit(2);
}

writeFileSync(versionFile, `${version}\n`);
console.log(`VERSION = ${version}`);
for (const file of packageFiles()) {
  const text = readFileSync(file, "utf8");
  if (typeof JSON.parse(text).version !== "string") continue;
  // The top-level "version" is the first one at two-space indentation.
  const updated = text.replace(/^( {2}"version":\s*")[^"]*(")/m, `$1${version}$2`);
  if (JSON.parse(updated).version !== version) {
    console.error(`set-version: could not rewrite the version in ${relative(root, file)}`);
    process.exit(1);
  }
  writeFileSync(file, updated);
  console.log(`${relative(root, file)} = ${version}`);
}
