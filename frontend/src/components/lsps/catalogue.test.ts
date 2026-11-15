import { describe, expect, it } from "vitest";
import {
  defaultLSP,
  lspDisabledReason,
  sameTitle,
  shortNodeId,
  visibleCatalogue,
} from "src/components/lsps/catalogue";
import type { LSPCatalogueEntry } from "src/hooks/useLSPCatalogue";
import type { LSP } from "src/types";

const entry = (p: Partial<LSPCatalogueEntry>): LSPCatalogueEntry =>
  ({
    nodeKey: "a".repeat(64),
    pubkey: "",
    host: "",
    name: "",
    description: "",
    website: "",
    image: "",
    chain: "flokicoin",
    network: "regtest",
    status: "open",
    operatorPubkey: "",
    announcedAt: 0,
    expiresAt: 0,
    usable: true,
    invalidReason: "",
    inGraph: true,
    graphChannels: 0,
    graphCapacity: 0,
    trusted: false,
    lastCheckAt: 0,
    addrs: [],
    protocols: [2],
    lsps1: [],
    lsps2Fees: [],
    lsps5: [],
    languages: [],
    relays: [],
    ...p,
  }) as LSPCatalogueEntry;

const lsp = (p: Partial<LSP>): LSP =>
  ({
    nodeKey: "b".repeat(64),
    name: "",
    pubkey: "02" + "b".repeat(64),
    host: "",
    active: true,
    status: "open",
    usable: true,
    ...p,
  }) as LSP;

describe("LSP catalogue (U-26.UI1)", () => {
  it("hides closed and unusable ones, sorts by title, filters", () => {
    const all = [
      entry({ nodeKey: "c".repeat(64), name: "Zeta" }),
      entry({ nodeKey: "d".repeat(64), name: "Alpha", protocols: [1] }),
      entry({ nodeKey: "e".repeat(64), name: "Closed", status: "closed" }),
      entry({ nodeKey: "f".repeat(64), name: "Broken", usable: false }),
      entry({ nodeKey: "1".repeat(64), name: "Far", inGraph: false }),
    ];
    const v = visibleCatalogue(all, {});
    expect(v.inGraph.map((l) => l.name)).toEqual(["Alpha", "Zeta"]);
    expect(v.notInGraph.map((l) => l.name)).toEqual(["Far"]);
    expect(
      visibleCatalogue(all, { protocol: 1 }).inGraph.map((l) => l.name)
    ).toEqual(["Alpha"]);
    expect(
      visibleCatalogue(all, { text: "zet" }).inGraph.map((l) => l.name)
    ).toEqual(["Zeta"]);
  });
  it("marks a title claimed twice", () => {
    const dupes = sameTitle([
      entry({ nodeKey: "c".repeat(64), name: "Alice" }),
      entry({ nodeKey: "d".repeat(64), name: "alice" }),
      entry({ nodeKey: "e".repeat(64), name: "Bob" }),
    ]);
    expect(dupes.has("alice")).toBe(true);
    expect(dupes.has("bob")).toBe(false);
  });
  it("shows a short node id", () => {
    expect(shortNodeId("0123456789abcdef0123")).toBe("01234567…cdef0123");
  });
});

describe("LSP dropdowns (U-26.UI3, UI4)", () => {
  it("disables paused, closed, unannounced and unresolved LSPs", () => {
    expect(lspDisabledReason(lsp({}))).toBeUndefined();
    expect(lspDisabledReason(lsp({ status: "paused" }))).toBe("paused");
    expect(lspDisabledReason(lsp({ status: "closed" }))).toBe("closed");
    expect(lspDisabledReason(lsp({ usable: false }))).toBe("notAnnounced");
    expect(lspDisabledReason(lsp({ pubkey: "" }))).toBe("unreachable");
  });
  it("defaults to the last used selectable LSP, else the first", () => {
    const a = lsp({ pubkey: "02" + "a".repeat(64) });
    const b = lsp({ pubkey: "02" + "c".repeat(64) });
    const paused = lsp({ pubkey: "02" + "d".repeat(64), status: "paused" });
    expect(defaultLSP([a, b], b.pubkey)).toBe(b.pubkey);
    expect(defaultLSP([paused, a], paused.pubkey)).toBe(a.pubkey);
    expect(defaultLSP([], null)).toBe("");
  });
});
