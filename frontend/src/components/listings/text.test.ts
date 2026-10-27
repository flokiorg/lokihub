import { describe, expect, it } from "vitest";
import {
  MAX_TITLE,
  capText,
  graphemes,
  validateListingForm,
} from "src/components/listings/text";

const ok = {
  title: "Cash club",
  content: "",
  image: "",
  location: "Lisbon, Baixa",
  languages: "en, pt",
  regions: "eyc, eycd",
  operator: "",
  operatorRelay: "",
};

describe("listing form (WP-19 item 13, U-25.RC3)", () => {
  it("accepts a valid form", () => {
    expect(validateListingForm(ok)).toBeUndefined();
  });
  it("caps the title at 40 user-perceived characters on one line", () => {
    expect(
      validateListingForm({ ...ok, title: "é".repeat(MAX_TITLE) })
    ).toBeUndefined();
    expect(validateListingForm({ ...ok, title: "a".repeat(41) })).toBe("title");
    expect(validateListingForm({ ...ok, title: "a\nb" })).toBe("title");
    // A family emoji is one character.
    expect(graphemes("👨‍👩‍👧")).toBe(1);
    expect(capText("abc", 2)).toBe("ab");
  });
  it("takes an https image only", () => {
    expect(validateListingForm({ ...ok, image: "http://x.test/a.png" })).toBe(
      "image"
    );
    expect(
      validateListingForm({ ...ok, image: "https://x.test/a.png" })
    ).toBeUndefined();
  });
  it("takes geohash prefixes of 3 or 4 characters", () => {
    for (const g of ["ey", "eycde", "eycdef", "aaa"]) {
      expect(validateListingForm({ ...ok, regions: g })).toBe("regions");
    }
  });
  it("takes a one-line place with no link", () => {
    expect(validateListingForm({ ...ok, location: "see https://x" })).toBe(
      "location"
    );
  });
  it("takes two-letter languages and a hex or npub operator", () => {
    expect(validateListingForm({ ...ok, languages: "eng" })).toBe("languages");
    expect(validateListingForm({ ...ok, operator: "nothex" })).toBe("operator");
    expect(
      validateListingForm({
        ...ok,
        operator:
          "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5",
      })
    ).toBeUndefined();
    expect(
      validateListingForm({
        ...ok,
        operator:
          "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5",
        operatorRelay: "https://x",
      })
    ).toBe("operatorRelay");
  });
});
