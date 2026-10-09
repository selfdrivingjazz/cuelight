#!/usr/bin/env node
// CI guard: VERSION is valid semver and every workspace package.json agrees.
//
//   node scripts/check-version.mjs [--expect 0.3.0]
//
// --expect additionally requires VERSION to equal the given value (e.g. the
// pushed tag without its leading "v").
import { readFileSync } from "node:fs";
import { relative } from "node:path";
import { packageFiles, readVersion, root, semver } from "./versioning.mjs";

const problems = [];
const version = readVersion();
if (!semver.test(version)) problems.push(`VERSION "${version}" is not MAJOR.MINOR.PATCH[-PRERELEASE]`);
const expectIndex = process.argv.indexOf("--expect");
if (expectIndex !== -1) {
  const expected = (process.argv[expectIndex + 1] ?? "").replace(/^v/, "");
  if (expected !== version) problems.push(`VERSION is ${version} but ${expected || "(missing --expect value)"} was expected`);
}
for (const file of packageFiles()) {
  const declared = JSON.parse(readFileSync(file, "utf8")).version;
  if (declared !== undefined && declared !== version) problems.push(`${relative(root, file)} has version ${declared}, VERSION is ${version}`);
}
if (problems.length > 0) {
  for (const problem of problems) console.error(`check-version: ${problem}`);
  console.error("Fix with: node scripts/set-version.mjs <version>");
  process.exit(1);
}
console.log(`check-version: ${version} is consistent`);
