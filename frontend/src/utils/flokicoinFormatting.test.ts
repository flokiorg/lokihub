import { describe, expect, it } from "vitest";

import {
  FLOKICOIN_DISPLAY_FORMAT_AUTO,
  FLOKICOIN_DISPLAY_FORMAT_FLC,
  FLOKICOIN_DISPLAY_FORMAT_LOKI,
} from "src/constants";
import {
  FLOKICOIN_NATIVE_ASSET,
  NativeAssetFormat,
  displayToLoki,
  formatFlokicoinAmount,
  formatNativeAmount,
  getFlokicoinUnit,
  lokiToDisplay,
} from "src/utils/flokicoinFormatting";

// Regression guard (FIX #2): after generalizing to formatNativeAmount, the
// formatFlokicoinAmount wrapper MUST still produce byte-for-byte identical
// output. Amounts are in milliloki; cases avoid grouping separators so the
// expected strings are locale-stable.
describe("formatFlokicoinAmount (regression: unchanged Flokicoin output)", () => {
  it("formats ticker (FLC) amounts", () => {
    // 150,000,000 mloki = 150,000 loki = 0.0015 FLC
    expect(
      formatFlokicoinAmount(150_000_000, FLOKICOIN_DISPLAY_FORMAT_FLC)
    ).toBe("0.0015 FLC");
    expect(
      formatFlokicoinAmount(150_000_000, FLOKICOIN_DISPLAY_FORMAT_FLC, false)
    ).toBe("0.0015");
  });

  it("formats loki amounts", () => {
    // 500,000 mloki = 500 loki
    expect(formatFlokicoinAmount(500_000, FLOKICOIN_DISPLAY_FORMAT_LOKI)).toBe(
      "500 loki"
    );
    expect(
      formatFlokicoinAmount(1_000, FLOKICOIN_DISPLAY_FORMAT_LOKI, false)
    ).toBe("1");
  });

  it("AUTO flips at the 100,000 loki threshold", () => {
    // 50,000 mloki = 50 loki (below) -> loki
    expect(formatFlokicoinAmount(50_000, FLOKICOIN_DISPLAY_FORMAT_AUTO)).toBe(
      "50 loki"
    );
    // 20,000,000,000 mloki = 20,000,000 loki (above) -> FLC = 0.2 FLC
    expect(
      formatFlokicoinAmount(20_000_000_000, FLOKICOIN_DISPLAY_FORMAT_AUTO)
    ).toBe("0.2 FLC");
  });
});

// Acceptance (design §3.4): overriding native_asset flips all unit/ticker text
// with no code change. A throwaway BTC/sat asset must render "sat" units and a
// "BTC" ticker with the same 8-decimal shape.
describe("formatNativeAmount (chain-neutral: a BTC/sat asset)", () => {
  const BTC: NativeAssetFormat = {
    ticker: "BTC",
    symbol: "₿",
    unitName: "sat",
    subunitName: "msat",
    decimals: 8,
    subunitPerUnit: 1000,
    autoThresholdLoki: 100_000,
    bech32Hrp: "bc",
    explorer: {
      label: "mempool.space",
      url: "https://mempool.space",
      txPath: "/tx/{txid}",
    },
  };

  it("renders sat units and the BTC ticker", () => {
    // 500,000 msat = 500 sat (LOKI here is the chain-neutral "base unit" format)
    expect(formatNativeAmount(500_000, FLOKICOIN_DISPLAY_FORMAT_LOKI, BTC)).toBe(
      "500 sat"
    );
    // 150,000,000 msat = 150,000 sat = 0.0015 BTC
    expect(
      formatNativeAmount(150_000_000, FLOKICOIN_DISPLAY_FORMAT_FLC, BTC)
    ).toBe("0.0015 BTC");
  });

  it("defaults to the embedded Flokicoin asset when none is passed", () => {
    expect(formatNativeAmount(150_000_000, FLOKICOIN_DISPLAY_FORMAT_FLC)).toBe(
      "0.0015 FLC"
    );
    expect(FLOKICOIN_NATIVE_ASSET.ticker).toBe("FLC");
  });
});

// Sibling helpers: numerically identical for Flokicoin, generalized for BTC.
describe("getFlokicoinUnit / lokiToDisplay / displayToLoki", () => {
  const BTC: NativeAssetFormat = {
    ticker: "BTC",
    symbol: "₿",
    unitName: "sat",
    subunitName: "msat",
    decimals: 8,
    subunitPerUnit: 1000,
    autoThresholdLoki: 100_000,
    bech32Hrp: "bc",
    explorer: {
      label: "mempool.space",
      url: "https://mempool.space",
      txPath: "/tx/{txid}",
    },
  };

  it("getFlokicoinUnit returns ticker vs base unit (Flokicoin, unchanged)", () => {
    expect(getFlokicoinUnit(FLOKICOIN_DISPLAY_FORMAT_FLC)).toBe("FLC");
    expect(getFlokicoinUnit(FLOKICOIN_DISPLAY_FORMAT_LOKI)).toBe("loki");
    expect(getFlokicoinUnit(FLOKICOIN_DISPLAY_FORMAT_AUTO, 50)).toBe("loki");
    expect(getFlokicoinUnit(FLOKICOIN_DISPLAY_FORMAT_AUTO, 100_000)).toBe("FLC");
  });

  it("lokiToDisplay / displayToLoki (Flokicoin, unchanged)", () => {
    expect(lokiToDisplay(100_000_000, FLOKICOIN_DISPLAY_FORMAT_FLC)).toBe(1);
    expect(lokiToDisplay(500, FLOKICOIN_DISPLAY_FORMAT_LOKI)).toBe(500);
    expect(displayToLoki(1, FLOKICOIN_DISPLAY_FORMAT_FLC)).toBe(100_000_000);
    expect(displayToLoki(500, FLOKICOIN_DISPLAY_FORMAT_LOKI)).toBe(500);
  });

  it("generalizes to a BTC/sat asset", () => {
    expect(getFlokicoinUnit(FLOKICOIN_DISPLAY_FORMAT_FLC, 0, BTC)).toBe("BTC");
    expect(getFlokicoinUnit(FLOKICOIN_DISPLAY_FORMAT_LOKI, 0, BTC)).toBe("sat");
    expect(lokiToDisplay(100_000_000, FLOKICOIN_DISPLAY_FORMAT_FLC, BTC)).toBe(1);
    expect(displayToLoki(1, FLOKICOIN_DISPLAY_FORMAT_FLC, BTC)).toBe(100_000_000);
  });
});
