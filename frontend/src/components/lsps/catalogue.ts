// Pure helpers of the LSP catalogue view (WP-26), kept apart for the tests.

import type { LSPCatalogueEntry } from "src/hooks/useLSPCatalogue";
import type { LSP } from "src/types";

// shortNodeId shows the node key's start and end, next to every claimed title
// (NIP-LSP §Security Considerations, copied names).
export function shortNodeId(key: string): string {
  return key.length > 16 ? `${key.slice(0, 8)}…${key.slice(-8)}` : key;
}

// visibleCatalogue is what Settings shows: no closed or unusable announcement,
// alphabetical by title then key, filtered by protocol and text, those in this
// node's graph first (the others are shown collapsed).
export function visibleCatalogue(
  all: LSPCatalogueEntry[],
  filter: { protocol?: number; text?: string }
): { inGraph: LSPCatalogueEntry[]; notInGraph: LSPCatalogueEntry[] } {
  const text = (filter.text ?? "").trim().toLowerCase();
  const rows = all
    .filter((l) => l.trusted || (l.usable && l.status !== "closed"))
    .filter(
      (l) => !filter.protocol || (l.protocols ?? []).includes(filter.protocol)
    )
    .filter(
      (l) =>
        !text ||
        l.name.toLowerCase().includes(text) ||
        l.nodeKey.includes(text) ||
        l.website.toLowerCase().includes(text)
    )
    .sort(
      (a, b) =>
        a.name.localeCompare(b.name) || a.nodeKey.localeCompare(b.nodeKey)
    );
  return {
    inGraph: rows.filter((l) => l.inGraph || l.trusted),
    notInGraph: rows.filter((l) => !l.inGraph && !l.trusted),
  };
}

// sameTitle are the titles more than one LSP claims, to be marked.
export function sameTitle(all: LSPCatalogueEntry[]): Set<string> {
  const seen = new Map<string, number>();
  for (const l of all) {
    if (l.name) {
      seen.set(l.name.toLowerCase(), (seen.get(l.name.toLowerCase()) ?? 0) + 1);
    }
  }
  return new Set([...seen].filter(([, n]) => n > 1).map(([k]) => k));
}

// lspDisabledReason is why a trusted LSP cannot be picked for a new purchase
// now, or undefined when it can (NIP-LSP §Client Behavior).
export function lspDisabledReason(
  l: LSP
): "paused" | "closed" | "notAnnounced" | "unreachable" | undefined {
  if (!l.usable) {
    return "notAnnounced";
  }
  if (l.status === "closed") {
    return "closed";
  }
  if (l.status === "paused") {
    return "paused";
  }
  if (!l.pubkey) {
    return "unreachable";
  }
  return undefined;
}

const LAST_USED_KEY = "lokihub-last-lsp";

// defaultLSP is the last used selectable LSP, else the first selectable one.
export function defaultLSP(lsps: LSP[], lastUsed?: string | null): string {
  const selectable = lsps.filter((l) => !lspDisabledReason(l));
  if (lastUsed && selectable.some((l) => l.pubkey === lastUsed)) {
    return lastUsed;
  }
  return selectable[0]?.pubkey ?? "";
}

export function readLastUsedLSP(): string | null {
  try {
    return localStorage.getItem(LAST_USED_KEY);
  } catch {
    return null;
  }
}

export function rememberLSP(pubkey: string) {
  try {
    localStorage.setItem(LAST_USED_KEY, pubkey);
  } catch {
    // per-viewer convenience only
  }
}
