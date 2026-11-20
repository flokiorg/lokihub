import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import AssistantGroupPicker from "src/components/connections/AssistantGroupPicker";
import {
  AI_GROUPS,
  AIGroup,
  AssistantLevels,
  DEFAULT_ASSISTANT_LEVELS,
  levelsToGrants,
} from "src/hooks/useAIAssistants";

const ALL_GROUPS: AIGroup[] = [
  "wallet",
  "cash_circles",
  "channels",
  "listings",
  "node",
  "connections",
];

const levels = (p: Partial<AssistantLevels>): AssistantLevels => ({
  wallet: "off",
  cash_circles: "off",
  channels: "off",
  listings: "off",
  node: "off",
  connections: "off",
  ...p,
});

describe("DEFAULT_ASSISTANT_LEVELS", () => {
  it("covers every group and is least-privilege (read-only wallet + node)", () => {
    expect(Object.keys(DEFAULT_ASSISTANT_LEVELS).sort()).toEqual(
      [...ALL_GROUPS].sort()
    );
    expect(levelsToGrants(DEFAULT_ASSISTANT_LEVELS)).toEqual({
      wallet: "read",
      node: "read",
    });
    expect(Object.values(DEFAULT_ASSISTANT_LEVELS)).not.toContain("write");
  });
});

describe("levelsToGrants", () => {
  it("drops groups that are off", () => {
    expect(levelsToGrants(levels({}))).toEqual({});
  });

  it("maps read and write through unchanged", () => {
    expect(
      levelsToGrants(
        levels({ wallet: "write", listings: "read", connections: "write" })
      )
    ).toEqual({ wallet: "write", listings: "read", connections: "write" });
  });

  it("grants every group when all are on", () => {
    const all = levels(
      Object.fromEntries(
        ALL_GROUPS.map((g) => [g, "write"])
      ) as Partial<AssistantLevels>
    );
    const g = levelsToGrants(all);
    expect(Object.keys(g).sort()).toEqual([...ALL_GROUPS].sort());
    expect(new Set(Object.values(g))).toEqual(new Set(["write"]));
  });

  it("does not mutate its input", () => {
    const input = levels({ node: "read" });
    const copy = { ...input };
    levelsToGrants(input);
    expect(input).toEqual(copy);
  });
});

describe("AI_GROUPS", () => {
  it("describes each of the six groups exactly once, with read text and a write risk", () => {
    expect(AI_GROUPS.map((g) => g.id).sort()).toEqual([...ALL_GROUPS].sort());
    for (const g of AI_GROUPS) {
      expect(g.title).not.toBe("");
      expect(g.read).not.toBe("");
      expect(g.writeRisk.length).toBeGreaterThan(20);
    }
  });

  it("flags only channels and node as advanced", () => {
    expect(
      AI_GROUPS.filter((g) => g.advanced)
        .map((g) => g.id)
        .sort()
    ).toEqual(["channels", "node"]);
  });
});

// Rendered server-side: the vitest setup is node-only (no jsdom /
// @testing-library), so this checks markup, not interaction.
describe("AssistantGroupPicker (static render)", () => {
  // react-i18next warns that no i18n instance is set; t() then returns the
  // default strings, which is what these assertions use.
  let warn: ReturnType<typeof vi.spyOn>;
  beforeAll(() => {
    warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  });
  afterAll(() => warn.mockRestore());

  const render = (l: AssistantLevels) =>
    renderToStaticMarkup(
      createElement(AssistantGroupPicker, { levels: l, onChange: () => {} })
    );

  it("renders every group with Off / Read / Read & change options", () => {
    const html = render(DEFAULT_ASSISTANT_LEVELS);
    for (const g of AI_GROUPS) {
      expect(html).toContain(g.title.replace("&", "&amp;"));
    }
    expect(html.match(/>Off</g)?.length).toBe(AI_GROUPS.length);
    expect(html.match(/>Read &amp; change</g)?.length).toBe(AI_GROUPS.length);
    expect(html.match(/>Advanced</g)?.length).toBe(2);
  });

  it("shows read scope for granted groups and 'No access.' for the rest", () => {
    const html = render(DEFAULT_ASSISTANT_LEVELS);
    const wallet = AI_GROUPS.find((g) => g.id === "wallet")!;
    expect(html).toContain(wallet.read);
    expect(html.match(/No access\./g)?.length).toBe(4);
  });

  it("shows the write risk only for groups set to Read & change", () => {
    const off = render(levels({}));
    for (const g of AI_GROUPS) {
      expect(off).not.toContain(g.writeRisk.slice(0, 30));
    }
    const html = render(levels({ cash_circles: "write", node: "read" }));
    const cash = AI_GROUPS.find((g) => g.id === "cash_circles")!;
    const node = AI_GROUPS.find((g) => g.id === "node")!;
    expect(html).toContain(cash.writeRisk.slice(0, 30));
    expect(html).not.toContain(node.writeRisk.slice(0, 30));
  });

  it("marks the chosen level as the active tab", () => {
    const html = render(levels({ listings: "write" }));
    const labels = [
      ...html.matchAll(/role="tab" aria-selected="true"[^>]*>([^<]*)</g),
    ].map((m) => m[1]);
    expect(labels.filter((l) => l === "Off").length).toBe(5);
    expect(labels.filter((l) => l === "Read &amp; change").length).toBe(1);
  });
});
