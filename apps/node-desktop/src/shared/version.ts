const semver = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/;

export function isVersion(value: string): boolean { return semver.test(value); }

/**
 * Orders semantic versions like internal/updates.CompareVersions: numeric core,
 * then a prerelease sorts before its release. Throws on malformed input.
 */
export function compareVersions(a: string, b: string): number {
  const left = semver.exec(a);
  const right = semver.exec(b);
  if (!left || !right) throw new Error(`invalid version ${left ? b : a}`);
  for (let index = 1; index <= 3; index += 1) {
    const difference = Number(left[index]) - Number(right[index]);
    if (difference !== 0) return Math.sign(difference);
  }
  const leftPre = left[4];
  const rightPre = right[4];
  if (leftPre === rightPre) return 0;
  if (leftPre === undefined) return 1;
  if (rightPre === undefined) return -1;
  const leftParts = leftPre.split(".");
  const rightParts = rightPre.split(".");
  for (let index = 0; index < Math.max(leftParts.length, rightParts.length); index += 1) {
    const l = leftParts[index];
    const r = rightParts[index];
    if (l === undefined) return -1;
    if (r === undefined) return 1;
    const ln = /^\d+$/.test(l);
    const rn = /^\d+$/.test(r);
    if (ln && rn && Number(l) !== Number(r)) return Math.sign(Number(l) - Number(r));
    if (ln !== rn) return ln ? -1 : 1;
    if (l !== r) return l < r ? -1 : 1;
  }
  return 0;
}
