import { describe, expect, it } from "vitest";
import { selectOutboxUrls } from "src/lib/nostrRelaySet";

const relaysFor = (author: string, n: number) =>
  Array.from({ length: n }, (_, i) => `wss://${author}-${i}.example`);

describe("selectOutboxUrls", () => {
  it("keeps every author's relays when all fit", () => {
    const urls = selectOutboxUrls([
      ["wss://a.example"],
      ["wss://b.example"],
      ["wss://c.example"],
    ]);
    expect(urls).toEqual([
      "wss://a.example",
      "wss://b.example",
      "wss://c.example",
    ]);
  });

  // The regression. The old code unioned every author's relays and THEN
  // sliced to six, so the first author whose relays came back filled the
  // whole budget and every author after them contributed nothing — on a page
  // listing many recipients, the later ones silently resolved to no profile.
  it("gives every author a share when the first one is greedy", () => {
    const greedy = relaysFor("greedy", 20);
    const urls = selectOutboxUrls([
      greedy,
      ["wss://second.example"],
      ["wss://third.example"],
    ]);

    expect(urls).toContain("wss://second.example");
    expect(urls).toContain("wss://third.example");
    // And the greedy author is still held to its own allowance.
    expect(urls.filter((u) => u.startsWith("wss://greedy-"))).toHaveLength(6);
  });

  it("caps each author at six", () => {
    const urls = selectOutboxUrls([relaysFor("solo", 50)]);
    expect(urls).toHaveLength(6);
  });

  it("still bounds the union for very many authors", () => {
    // Ten authors at six each would be sixty relays; the overall ceiling of
    // 24 exists so a page cannot open an unbounded number of connections.
    const urls = selectOutboxUrls(
      Array.from({ length: 10 }, (_, i) => relaysFor(`author${i}`, 6))
    );
    expect(urls).toHaveLength(24);
  });

  it("deduplicates relays shared between authors", () => {
    const urls = selectOutboxUrls([
      ["wss://shared.example", "wss://a.example"],
      ["wss://shared.example", "wss://b.example"],
    ]);
    expect(urls).toEqual([
      "wss://shared.example",
      "wss://a.example",
      "wss://b.example",
    ]);
  });

  it("handles authors with no known relays", () => {
    expect(selectOutboxUrls([])).toEqual([]);
    expect(selectOutboxUrls([[], []])).toEqual([]);
    expect(selectOutboxUrls([[], ["wss://only.example"]])).toEqual([
      "wss://only.example",
    ]);
  });
});
