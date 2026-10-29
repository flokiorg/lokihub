import { describe, expect, it } from "vitest";
import { qrValue } from "./qrValue";

describe("qrValue", () => {
  it("renders every bech32 credential in uppercase", () => {
    for (const s of [
      "cashhub1qqqsyqcyq5rqwzqf",
      "lokicash1qqqsyqcyq5rqwzqf",
      "circlehub1qqqsyqcyq5rqwzqf",
      "cashorder1qqqsyqcyq5rqwzqf",
    ]) {
      expect(qrValue(s)).toBe(s.toUpperCase());
    }
  });

  it("leaves anything else as it is", () => {
    for (const s of [
      "nostr+walletconnect://abc?relay=wss://r&secret=def",
      "lokicash1qqqsyqcyq5rqwzqf#00ff",
      "lnfc1qqqsyqcyq5rqwzqf",
      "cashhub1qqqbio",
      "CASHHUB1QQQSYQCYQ5RQWZQF",
      "",
    ]) {
      expect(qrValue(s)).toBe(s);
    }
  });
});
