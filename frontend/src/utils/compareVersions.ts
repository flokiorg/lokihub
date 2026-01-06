/**
 * Compares two semantic version strings according to semver 2.0.0 spec.
 * Supports formats like: v1.2.3, 1.2.3, v1.2.3-alpha, v1.2.3-beta.1, v1.2.3+build.123
 *
 * @param versionA - First version to compare
 * @param versionB - Second version to compare
 * @returns -1 if versionA < versionB, 0 if equal, 1 if versionA > versionB
 */
export function compareVersions(versionA: string, versionB: string): number {
  // Remove 'v' prefix if present
  const cleanA = versionA.replace(/^v/, "");
  const cleanB = versionB.replace(/^v/, "");

  // Split by '+' to separate build metadata (which is ignored in comparison)
  const [coreA] = cleanA.split("+");
  const [coreB] = cleanB.split("+");

  // Split by '-' to separate main version from pre-release
  const [mainA, preReleaseA] = coreA.split("-");
  const [mainB, preReleaseB] = coreB.split("-");

  // Compare main version (major.minor.patch)
  const partsA = mainA.split(".").map(Number);
  const partsB = mainB.split(".").map(Number);

  for (let i = 0; i < Math.max(partsA.length, partsB.length); i++) {
    const numA = partsA[i] || 0;
    const numB = partsB[i] || 0;

    if (numA > numB) return 1;
    if (numA < numB) return -1;
  }

  // If main versions are equal, compare pre-release versions
  // Per semver: a pre-release version has lower precedence than a normal version
  if (!preReleaseA && preReleaseB) return 1; // A is stable, B is pre-release
  if (preReleaseA && !preReleaseB) return -1; // A is pre-release, B is stable
  if (!preReleaseA && !preReleaseB) return 0; // Both stable and equal

  // Both have pre-release versions, compare them
  // Split by '.' to get identifiers (e.g., "alpha.1" -> ["alpha", "1"])
  const prePartsA = preReleaseA.split(".");
  const prePartsB = preReleaseB.split(".");

  for (let i = 0; i < Math.max(prePartsA.length, prePartsB.length); i++) {
    // Missing identifier has lower precedence
    if (i >= prePartsA.length) return -1;
    if (i >= prePartsB.length) return 1;

    const partA = prePartsA[i];
    const partB = prePartsB[i];

    // Try to compare as numbers first
    const numA = parseInt(partA, 10);
    const numB = parseInt(partB, 10);

    if (!isNaN(numA) && !isNaN(numB)) {
      if (numA > numB) return 1;
      if (numA < numB) return -1;
    } else {
      // Compare as strings (alphabetically)
      // Numeric identifiers always have lower precedence than non-numeric
      if (!isNaN(numA)) return -1;
      if (!isNaN(numB)) return 1;

      if (partA > partB) return 1;
      if (partA < partB) return -1;
    }
  }

  return 0;
}
