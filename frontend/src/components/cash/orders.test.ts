import { describe, expect, it } from "vitest";
import {
  ceilingExceedsOutbound,
  emptyTermsForm,
  formFromSettings,
  isOrderConnectionString,
  loki,
  orderStateVariant,
  parseTermsForm,
  parseTiers,
  paymentStateVariant,
  type OrderSettings,
  type TermsForm,
} from "src/components/cash/orders";

// U-19.8: the operator Sell-cash console (NIP-CO / WP-19 item 8).

// H-CO-004: the sell flow never offers the Hub's own cashhub1 pairing.
describe("isOrderConnectionString", () => {
  it("accepts only a cashorder1 string, never a cashhub1 one", () => {
    expect(isOrderConnectionString("cashorder1qqqq")).toBe(true);
    expect(isOrderConnectionString("  CASHORDER1QQQQ  ")).toBe(true);
    expect(isOrderConnectionString("cashhub1qqqq")).toBe(false);
    expect(isOrderConnectionString("CASHHUB1QQQQ")).toBe(false);
    expect(isOrderConnectionString("lokicash1qqqq")).toBe(false);
    expect(isOrderConnectionString("")).toBe(false);
  });
});

// H-CO-103: an on-chain ceiling above the node's Lightning outbound warns.
describe("ceilingExceedsOutbound", () => {
  it("warns only when a non-zero ceiling is above outbound", () => {
    expect(ceilingExceedsOutbound(2_000_000, 1_000_000)).toBe(true);
    expect(ceilingExceedsOutbound(1_000_000, 1_000_000)).toBe(false);
    expect(ceilingExceedsOutbound(1_000_000, 2_000_000)).toBe(false);
    expect(ceilingExceedsOutbound(0, 0)).toBe(false);
    expect(ceilingExceedsOutbound(1, 0)).toBe(true);
  });
});

describe("state variants", () => {
  it("colours order states", () => {
    expect(orderStateVariant("minted")).toBe("positive");
    expect(orderStateVariant("paid")).toBe("outline");
    expect(orderStateVariant("open")).toBe("secondary");
    expect(orderStateVariant("expired")).toBe("warning");
    expect(orderStateVariant("closed")).toBe("warning");
  });

  it("colours payment states", () => {
    expect(paymentStateVariant("minted")).toBe("positive");
    expect(paymentStateVariant("confirmed")).toBe("outline");
    expect(paymentStateVariant("seen")).toBe("secondary");
    expect(paymentStateVariant("reorged")).toBe("warning");
    expect(paymentStateVariant("dropped")).toBe("destructive");
  });
});

describe("loki", () => {
  it("formats millis with its unit", () => {
    expect(loki(21_063_000)).toBe("21063 loki");
    expect(loki(1)).toBe("0.001 loki");
    expect(loki(0)).toBe("0 loki");
  });
});

describe("parseTiers", () => {
  it("takes ascending tiers with only the last open", () => {
    expect(
      parseTiers([
        { upToLoki: "1000", n: "1" },
        { upToLoki: "5000", n: "3" },
        { upToLoki: "", n: "6" },
      ]).tiers
    ).toEqual([
      { up_to_millis: 1_000_000, n: 1 },
      { up_to_millis: 5_000_000, n: 3 },
      { n: 6 },
    ]);
  });

  it("refuses a non-ascending up_to, a non-last open tier and n below one", () => {
    expect(
      parseTiers([
        { upToLoki: "5000", n: "1" },
        { upToLoki: "1000", n: "2" },
        { upToLoki: "", n: "3" },
      ]).error
    ).toBe("tiers");
    expect(
      parseTiers([
        { upToLoki: "", n: "1" },
        { upToLoki: "", n: "2" },
      ]).error
    ).toBe("tiers");
    expect(parseTiers([{ upToLoki: "", n: "0" }]).error).toBe("tiers");
    expect(parseTiers([{ upToLoki: "1000.5", n: "1" }, { upToLoki: "", n: "2" }]).error).toBe("tiers");
  });
});

const baseForm = (over: Partial<TermsForm> = {}): TermsForm => ({
  ...emptyTermsForm(),
  ...over,
});

describe("parseTermsForm", () => {
  it("sells nothing when both kinds are off, keeping the bill terms", () => {
    const r = parseTermsForm(
      baseForm({ redeemFeePpm: "5000", minTransferLoki: "1" })
    );
    expect(r.settings).toEqual({
      terms: {
        bill: {
          redeem_fee_base: 0,
          redeem_fee_ppm: 5000,
          min_transfer_millis: 1000,
        },
      },
      openCeilingLightningMillis: 0,
      openCeilingOnchainMillis: 0,
      openOnchainMax: 0,
    });
  });

  it("builds Lightning terms with its required ceiling", () => {
    const r = parseTermsForm(
      baseForm({
        lightning: {
          enabled: true,
          minLoki: "1",
          maxLoki: "1000",
          feeBaseLoki: "0.5",
          feePpm: "1000",
          window: "3600",
        },
        ceilingLightningLoki: "5000",
      })
    );
    expect(r.settings?.terms.lightning).toEqual({
      min_millis: 1000,
      max_millis: 1_000_000,
      fee_base: 500,
      fee_ppm: 1000,
      window: 3600,
    });
    expect(r.settings?.openCeilingLightningMillis).toBe(5_000_000);
  });

  it("builds on-chain terms with tiers and a whole-unit ceiling", () => {
    const r = parseTermsForm(
      baseForm({
        onchain: {
          enabled: true,
          minLoki: "10",
          maxLoki: "1000",
          feeBaseLoki: "21",
          feePpm: "2000",
          window: "86400",
        },
        tiers: [
          { upToLoki: "100", n: "1" },
          { upToLoki: "", n: "6" },
        ],
        ceilingOnchainLoki: "5000",
      })
    );
    expect(r.settings?.terms.onchain?.confirmations).toEqual([
      { up_to_millis: 100_000, n: 1 },
      { n: 6 },
    ]);
    expect(r.settings?.openCeilingOnchainMillis).toBe(5_000_000);
  });

  it("refuses rather than clamps: min above max, a bad ppm, a missing ceiling", () => {
    expect(
      parseTermsForm(
        baseForm({
          lightning: {
            enabled: true,
            minLoki: "1000",
            maxLoki: "1",
            feeBaseLoki: "0",
            feePpm: "0",
            window: "3600",
          },
          ceilingLightningLoki: "5000",
        })
      ).error
    ).toBe("lightningMinAboveMax");
    expect(
      parseTermsForm(
        baseForm({
          lightning: {
            enabled: true,
            minLoki: "1",
            maxLoki: "10",
            feeBaseLoki: "0",
            feePpm: "1000001",
            window: "3600",
          },
          ceilingLightningLoki: "5000",
        })
      ).error
    ).toBe("lightningFeePpm");
    expect(
      parseTermsForm(
        baseForm({
          lightning: {
            enabled: true,
            minLoki: "1",
            maxLoki: "10",
            feeBaseLoki: "0",
            feePpm: "0",
            window: "3600",
          },
          ceilingLightningLoki: "",
        })
      ).error
    ).toBe("lightningCeiling");
  });

  it("refuses fractional on-chain amounts", () => {
    expect(
      parseTermsForm(
        baseForm({
          onchain: {
            enabled: true,
            minLoki: "10.5",
            maxLoki: "1000",
            feeBaseLoki: "0",
            feePpm: "0",
            window: "86400",
          },
          ceilingOnchainLoki: "5000",
        })
      ).error
    ).toBe("onchainWhole");
  });
});

describe("formFromSettings", () => {
  it("gives the not-selling-yet state for null terms", () => {
    const f = formFromSettings(null);
    expect(f.lightning.enabled).toBe(false);
    expect(f.onchain.enabled).toBe(false);
    expect(f.tiers).toEqual([{ upToLoki: "", n: "1" }]);
  });

  it("round-trips stored settings back through the form", () => {
    const s: OrderSettings = {
      terms: {
        lightning: {
          min_millis: 1000,
          max_millis: 1_000_000,
          fee_base: 500,
          fee_ppm: 1000,
          window: 3600,
        },
        onchain: {
          min_millis: 10_000,
          max_millis: 1_000_000,
          fee_base: 21_000,
          fee_ppm: 2000,
          window: 86400,
          confirmations: [{ up_to_millis: 100_000, n: 1 }, { n: 6 }],
        },
        bill: { redeem_fee_base: 0, redeem_fee_ppm: 5000, min_transfer_millis: 1000 },
      },
      openCeilingLightningMillis: 5_000_000,
      openCeilingOnchainMillis: 5_000_000,
      openOnchainMax: 3,
    };
    const parsed = parseTermsForm(formFromSettings(s));
    expect(parsed.settings).toEqual(s);
  });
});
