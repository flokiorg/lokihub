import { describe, expect, it } from "vitest";
import {
  primaryProfileLabel,
  secondaryProfileLabel,
} from "src/utils/nostrProfileLabel";

// A real 32-byte hex pubkey, so npub encoding actually succeeds rather than
// falling through the safeNpubEncode catch.
const PUBKEY =
  "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d";

describe("primaryProfileLabel", () => {
  it("prefers a display name", () => {
    expect(
      primaryProfileLabel(PUBKEY, { displayName: "Alice", name: "alice" })
    ).toBe("Alice");
  });

  it("falls back to the name", () => {
    expect(primaryProfileLabel(PUBKEY, { name: "alice" })).toBe("alice");
  });

  // The regression. NostrIdentityHeader used to go displayName -> name ->
  // npub, skipping nip05 entirely, so someone identified only by a nip05 was
  // headed by a bare npub while their actual name sat demoted in the muted
  // line below.
  it("uses the nip05 when there is no display name or name", () => {
    const label = primaryProfileLabel(PUBKEY, { nip05: "alice@example.com" });
    expect(label).toBe("alice@example.com");
    expect(label).not.toContain("npub");
  });

  it("falls back to the lightning address before the npub", () => {
    const label = primaryProfileLabel(PUBKEY, { lud16: "alice@getalby.com" });
    expect(label).toBe("alice@getalby.com");
    expect(label).not.toContain("npub");
  });

  it("falls back to a shortened npub when the profile says nothing", () => {
    expect(primaryProfileLabel(PUBKEY, {})).toContain("npub1");
    expect(primaryProfileLabel(PUBKEY, undefined)).toContain("npub1");
  });

  it("still returns something for an unencodable pubkey", () => {
    expect(primaryProfileLabel("not-hex", undefined)).toBeTruthy();
  });
});

describe("secondaryProfileLabel", () => {
  // The other half of the same bug: with no primary name, the nip05 IS the
  // heading, so repeating it underneath printed it twice.
  it("returns nothing when the nip05 is already the primary label", () => {
    expect(
      secondaryProfileLabel(PUBKEY, { nip05: "alice@example.com" })
    ).toBeUndefined();
    expect(secondaryProfileLabel(PUBKEY, {})).toBeUndefined();
    expect(secondaryProfileLabel(PUBKEY, undefined)).toBeUndefined();
  });

  it("carries the nip05 under a real name", () => {
    const secondary = secondaryProfileLabel(PUBKEY, {
      displayName: "Alice",
      nip05: "alice@example.com",
    });
    expect(secondary?.identifier).toBe("alice@example.com");
    expect(secondary?.npub).toContain("npub1");
  });

  it("prefers the nip05 over the lightning address", () => {
    expect(
      secondaryProfileLabel(PUBKEY, {
        name: "alice",
        nip05: "alice@example.com",
        lud16: "alice@getalby.com",
      })?.identifier
    ).toBe("alice@example.com");
  });

  // fullNpub is carried alongside the shortened one so a copy button yields
  // the untruncated value.
  it("keeps the full npub beside the shortened one", () => {
    const secondary = secondaryProfileLabel(PUBKEY, { name: "alice" });
    expect(secondary?.fullNpub.length).toBeGreaterThan(
      secondary!.npub.length
    );
    expect(secondary?.fullNpub).not.toContain("…");
  });
});
