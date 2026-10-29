import { describe, expect, it } from "vitest";
import {
  acceptedTerms,
  changedFields,
  fieldUnit,
  formatValue,
  needsConfirmation,
  termsFromError,
  withoutTerms,
} from "src/components/lsps/terms";
import type { LSPTerms } from "src/types";

const fee = (
  field: string,
  announced: string,
  live: string,
  verdict: "same" | "worse" | "better"
) => ({ field: `lsps2_fee[0].${field}`, announced, live, verdict });

const worse: LSPTerms = {
  worse: true,
  fields: [
    fee("min_fee", "2000000", "2000000", "same"),
    fee("max_payment_size", "4000000000", "5000000000", "better"),
    fee("proportional", "4000", "4001", "worse"),
  ],
};

// U-26.UI5: the terms-changed dialog.
describe("needsConfirmation", () => {
  it("asks again for live terms worse than announced", () => {
    expect(needsConfirmation(worse, undefined)).toBe(true);
    expect(needsConfirmation(worse, [])).toBe(true);
  });

  it("asks again while a worse value is accepted at another live value", () => {
    expect(
      needsConfirmation(worse, [
        { field: "lsps2_fee[0].proportional", live: "4000" },
      ])
    ).toBe(true);
    expect(
      needsConfirmation(worse, [
        { field: "lsps2_fee[0].min_fee", live: "4001" },
      ])
    ).toBe(true);
  });

  it("does not ask once every worse value is accepted at its live value", () => {
    expect(needsConfirmation(worse, acceptedTerms(worse))).toBe(false);
  });

  it("does not ask for the same or better terms, or none compared", () => {
    expect(
      needsConfirmation(
        { worse: false, fields: [fee("min_fee", "2", "1", "better")] },
        undefined
      )
    ).toBe(false);
    expect(needsConfirmation(undefined, undefined)).toBe(false);
    expect(needsConfirmation(null, null)).toBe(false);
  });
});

describe("acceptedTerms and changedFields", () => {
  it("accepts exactly the worse values at their live values", () => {
    expect(acceptedTerms(worse)).toEqual([
      { field: "lsps2_fee[0].proportional", live: "4001" },
    ]);
  });

  it("shows only the values that differ, worse first", () => {
    expect(changedFields(worse).map((f) => f.verdict)).toEqual([
      "worse",
      "better",
    ]);
  });
});

describe("termsFromError", () => {
  it("reads the terms of a purchase refused for worse terms", () => {
    expect(termsFromError({ body: { message: "x", terms: worse } })).toEqual(
      worse
    );
  });

  it("reads nothing from another error", () => {
    expect(termsFromError(new Error("boom"))).toBeNull();
    expect(termsFromError({ body: { message: "x" } })).toBeNull();
    expect(termsFromError(null)).toBeNull();
    expect(
      termsFromError({
        body: { message: "x", terms: { worse: false, fields: [] } },
      })
    ).toBeNull();
  });
});

describe("units", () => {
  it("shows LSPS2 amounts in mloki and LSPS1 balances in loki", () => {
    expect(fieldUnit("lsps2_fee[0].min_fee")).toBe("mloki");
    expect(fieldUnit("lsps2_fee[0].min_payment_size")).toBe("mloki");
    expect(fieldUnit("lsps2_fee[0].proportional")).toBe("ppm");
    expect(fieldUnit("lsps2_fee[0].min_lifetime")).toBe("blocks");
    expect(fieldUnit("lsps2_fee[0].max_client_to_self_delay")).toBe("blocks");
    expect(fieldUnit("max_channel_balance")).toBe("loki");
    expect(fieldUnit("max_channel_expiry_blocks")).toBe("blocks");
    expect(fieldUnit("min_required_channel_confirmations")).toBe("");
    expect(fieldUnit("supports_zero_channel_reserve")).toBe("");
  });

  it("shows a missing live value as a dash", () => {
    expect(formatValue("lsps2_fee[0].min_fee", "")).toBe("—");
    expect(formatValue("lsps2_fee[0].min_fee", "5")).toBe("5 mloki");
  });
});

describe("withoutTerms", () => {
  it("sends the fee params back without the comparison", () => {
    expect(withoutTerms({ promise: "p", terms: worse })).toEqual({
      promise: "p",
    });
  });
});
