import { safeDecodeToHex } from "src/utils/nostr";

// Caps and checks the listing form shares with the backend (WP-19 item 13;
// NIP-CASH §Offer Listing): user-perceived characters, one line, https images,
// ISO-639-1 languages and geohash prefixes of 3 or 4 characters.

export const MAX_TITLE = 40;
export const MAX_CONTENT = 280;

// Intl.Segmenter is ES2022; the build targets an older lib, so it is typed here.
type GraphemeSegmenter = {
  segment(s: string): Iterable<{ segment: string }>;
};
const SegmenterCtor = (
  Intl as unknown as {
    Segmenter?: new (
      locale: undefined,
      options: { granularity: "grapheme" }
    ) => GraphemeSegmenter;
  }
).Segmenter;
const segmenter = SegmenterCtor
  ? new SegmenterCtor(undefined, { granularity: "grapheme" })
  : undefined;

// graphemes counts user-perceived characters.
export function graphemes(s: string): number {
  if (!segmenter) {
    return Array.from(s).length;
  }
  let n = 0;
  for (const _ of segmenter.segment(s)) {
    n++;
  }
  return n;
}

// capText cuts s to max user-perceived characters.
export function capText(s: string, max: number): string {
  if (!segmenter) {
    return Array.from(s).slice(0, max).join("");
  }
  let out = "";
  let n = 0;
  for (const seg of segmenter.segment(s)) {
    if (n === max) {
      break;
    }
    out += seg.segment;
    n++;
  }
  return out;
}

export function isOneLine(s: string): boolean {
  return !/[\r\n\t\u0085\u2028\u2029]/.test(s);
}

export function isHttpsUrl(s: string): boolean {
  try {
    const u = new URL(s);
    return u.protocol === "https:" && u.host !== "";
  } catch {
    return false;
  }
}

export function isLanguage(s: string): boolean {
  return /^[a-z]{2}$/.test(s);
}

export function isRegion(s: string): boolean {
  return /^[0-9bcdefghjkmnpqrstuvwxyz]{3,4}$/.test(s);
}

export function isHexPubkey(s: string): boolean {
  return /^[0-9a-f]{64}$/.test(s);
}

// splitList reads a comma or space separated list.
export function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter((x) => x !== "");
}

// validateListingForm returns the first problem with the form, or undefined.
// Exported for the tests.
export type ListingProblem =
  | "title"
  | "content"
  | "image"
  | "location"
  | "languages"
  | "regions"
  | "operator"
  | "operatorRelay";

export function validateListingForm(f: {
  title: string;
  content: string;
  image: string;
  location: string;
  languages: string;
  regions: string;
  operator: string;
  operatorRelay: string;
}): ListingProblem | undefined {
  if (!isOneLine(f.title) || graphemes(f.title) > MAX_TITLE) {
    return "title";
  }
  if (graphemes(f.content) > MAX_CONTENT) {
    return "content";
  }
  if (f.image && !isHttpsUrl(f.image)) {
    return "image";
  }
  if (
    !isOneLine(f.location) ||
    graphemes(f.location) > MAX_TITLE ||
    f.location.includes("://")
  ) {
    return "location";
  }
  if (splitList(f.languages).some((l) => !isLanguage(l))) {
    return "languages";
  }
  if (splitList(f.regions).some((g) => !isRegion(g))) {
    return "regions";
  }
  if (f.operator && !isHexPubkey(safeDecodeToHex(f.operator) ?? "")) {
    return "operator";
  }
  if (f.operatorRelay && !/^wss?:\/\//.test(f.operatorRelay)) {
    return "operatorRelay";
  }
  return undefined;
}
