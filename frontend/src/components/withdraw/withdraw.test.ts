import { describe, expect, it } from "vitest";
import {
  formFromView,
  hubEnableBlocked,
  hubFeeMillis,
  lokiToMillis,
  millisToLoki,
  parseWithdrawForm,
  scriptTypeName,
  statusRows,
  type WithdrawForm,
  type WithdrawTermsView,
} from "src/components/withdraw/withdraw";

const form = (f: Partial<WithdrawForm>): WithdrawForm => ({
  enabled: true,
  feeBaseLoki: "500",
  feePpm: "2000",
  minLoki: "",
  maxLoki: "",
  ...f,
});

// U-19.10: the per-hub on-chain withdrawal card.
describe("parseWithdrawForm", () => {
  it("takes the operator's inputs in millis", () => {
    expect(
      parseWithdrawForm(form({ minLoki: "1000", maxLoki: "2000.5" }))
    ).toEqual({
      input: {
        enabled: true,
        fee_base_millis: 500000,
        fee_ppm: 2000,
        min_millis: 1000000,
        max_millis: 2000500,
      },
    });
  });

  it("leaves an empty minimum or maximum out, and an empty fee as zero", () => {
    expect(parseWithdrawForm(form({ feeBaseLoki: "", feePpm: "" }))).toEqual({
      input: { enabled: true, fee_base_millis: 0, fee_ppm: 0 },
    });
  });

  it("takes the ppm bound 1000000 and refuses one above it", () => {
    expect(parseWithdrawForm(form({ feePpm: "1000000" })).input?.fee_ppm).toBe(
      1000000
    );
    expect(parseWithdrawForm(form({ feePpm: "1000001" })).error).toBe("feePpm");
  });

  it("refuses negative, fractional and malformed values rather than clamp", () => {
    expect(parseWithdrawForm(form({ feePpm: "-1" })).error).toBe("feePpm");
    expect(parseWithdrawForm(form({ feePpm: "1.5" })).error).toBe("feePpm");
    expect(parseWithdrawForm(form({ feeBaseLoki: "-1" })).error).toBe(
      "feeBase"
    );
    expect(parseWithdrawForm(form({ feeBaseLoki: "0.0001" })).error).toBe(
      "feeBase"
    );
    expect(parseWithdrawForm(form({ minLoki: "x" })).error).toBe("min");
    expect(parseWithdrawForm(form({ maxLoki: "1e3" })).error).toBe("max");
  });

  it("takes a minimum equal to the maximum and refuses one above it", () => {
    expect(
      parseWithdrawForm(form({ minLoki: "10", maxLoki: "10" })).input
    ).toMatchObject({ min_millis: 10000, max_millis: 10000 });
    expect(
      parseWithdrawForm(form({ minLoki: "10.001", maxLoki: "10" })).error
    ).toBe("minAboveMax");
  });
});

describe("loki and millis", () => {
  it("converts both ways with three decimals", () => {
    expect(lokiToMillis("1.234")).toBe(1234);
    expect(lokiToMillis("0.5")).toBe(500);
    expect(lokiToMillis("7")).toBe(7000);
    expect(lokiToMillis(" 7 ")).toBe(7000);
    expect(lokiToMillis("1.2345")).toBeNull();
    expect(lokiToMillis("")).toBeNull();
    expect(millisToLoki(1234)).toBe("1.234");
    expect(millisToLoki(500)).toBe("0.5");
    expect(millisToLoki(7000)).toBe("7");
    expect(millisToLoki(1)).toBe("0.001");
  });

  it("reads the stored terms back into the form", () => {
    const view: WithdrawTermsView = {
      enabled: false,
      fee_base_millis: 500,
      fee_ppm: 2000,
      max_millis: 21000000,
      script_types: [[0, 20]],
      final_depth: 6,
      node_enabled: true,
      capable: true,
      offered: false,
    };
    const f = formFromView(view);
    expect(f).toEqual({
      enabled: false,
      feeBaseLoki: "0.5",
      feePpm: "2000",
      minLoki: "",
      maxLoki: "21000",
    });
    expect(parseWithdrawForm(f).input).toEqual({
      enabled: false,
      fee_base_millis: 500,
      fee_ppm: 2000,
      max_millis: 21000000,
    });
  });
});

describe("hubFeeMillis", () => {
  it("is NIP-CASH's worked example, network fee aside", () => {
    expect(
      hubFeeMillis({ fee_base_millis: 500000, fee_ppm: 2000 }, 21000000)
    ).toBe(542000);
    expect(hubFeeMillis({ fee_base_millis: 0, fee_ppm: 1 }, 999999)).toBe(0);
    expect(hubFeeMillis({ fee_base_millis: 0, fee_ppm: 1 }, 1000000)).toBe(1);
  });
});

describe("read-only values", () => {
  it("names the script types paid", () => {
    expect(
      [
        [0, 20],
        [0, 32],
        [1, 32],
        [2, 40],
      ].map((p) => scriptTypeName(p as [number, number]))
    ).toEqual(["P2WPKH", "P2WSH", "P2TR", "v2/40"]);
  });

  it("says why a hub cannot enable withdrawals", () => {
    expect(hubEnableBlocked({ node_enabled: true, capable: true })).toBeNull();
    expect(hubEnableBlocked({ node_enabled: false, capable: true })).toBe(
      "nodeOff"
    );
    expect(hubEnableBlocked({ node_enabled: true, capable: false })).toBe(
      "notCapable"
    );
  });
});

// U-19.11: the node status panel.
describe("statusRows", () => {
  const status = {
    ready: true,
    payouts: { sent: 2, confirmed: 5 },
    unconfirmed_millis: 1500,
    set_aside_sat: 10,
    eligible_sat: 100,
    floor_sat: 5,
    oldest_unbroadcast_secs: 0,
    bumps_total: 1,
    reorgs_total: 0,
    invalidations_total: 0,
    coin_vanished_total: 0,
    invariant_failures_total: 0,
  };

  it("lists every value with its unit, payouts by state", () => {
    const rows = Object.fromEntries(
      statusRows(status).map((r) => [r.key, r.value])
    );
    expect(rows).toMatchObject({
      eligible: "100 loki",
      setAside: "10 loki",
      floor: "5 loki",
      unconfirmed: "1.5 loki",
      payouts: "confirmed 5, sent 2",
      bumps: "1",
    });
    expect(statusRows(status).some((r) => r.alert)).toBe(false);
  });

  it("flags a vanished coin or a failed invariant", () => {
    const rows = statusRows({
      ...status,
      payouts: null,
      coin_vanished_total: 1,
      invariant_failures_total: 2,
    });
    expect(rows.filter((r) => r.alert).map((r) => r.key)).toEqual([
      "coinsVanished",
      "invariantFailures",
    ]);
    expect(rows.find((r) => r.key === "payouts")?.value).toBe("0");
  });
});
