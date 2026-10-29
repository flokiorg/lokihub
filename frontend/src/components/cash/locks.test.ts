import { describe, expect, it } from "vitest";
import { groupLock, ownerLock } from "src/components/cash/locks";

const slice = (p: Partial<Parameters<typeof ownerLock>[0]>) => ({
  claimed: false,
  archived: false,
  ...p,
});

// U-19.9 / WP-16 item 8: ordered and re-minted slices cannot be removed.
describe("ownerLock", () => {
  it("locks a live unclaimed re-minted or ordered slice", () => {
    expect(ownerLock(slice({ reminted: true }))).toBe("reminted");
    expect(ownerLock(slice({ ordered: true }))).toBe("ordered");
    expect(ownerLock(slice({ ordered: true, reminted: true }))).toBe(
      "reminted"
    );
  });

  it("leaves other slices, claimed ones and archived rows unlocked", () => {
    expect(ownerLock(slice({}))).toBeNull();
    expect(ownerLock(slice({ reminted: true, claimed: true }))).toBeNull();
    expect(ownerLock(slice({ ordered: true, archived: true }))).toBeNull();
  });

  it("locks a bill when any of its slices is locked", () => {
    expect(groupLock([slice({}), slice({ reminted: true })])).toBe("reminted");
    expect(
      groupLock([slice({}), slice({ claimed: true, ordered: true })])
    ).toBeNull();
    expect(groupLock([])).toBeNull();
  });
});
