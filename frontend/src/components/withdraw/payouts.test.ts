import { describe, expect, it } from "vitest";
import {
  cancelNote,
  depthLabel,
  feeRate,
  loki,
  outpoint,
  overdue,
  shorten,
  stateVariant,
} from "src/components/withdraw/payouts";

const txid = "a".repeat(64);

// U-19.10: the payouts list and detail.
describe("payout display", () => {
  it("shows the output as txid:vout only once broadcast", () => {
    expect(outpoint({ txid, vout: 1 })).toBe(`${shorten(txid)}:1`);
    expect(outpoint({ txid, vout: 0 })).toBe(`${shorten(txid)}:0`);
    expect(outpoint({})).toBeNull();
    expect(outpoint({ txid })).toBeNull();
  });

  it("counts depth up to final_depth", () => {
    expect(depthLabel({ txid, depth: 3, final_depth: 6 })).toBe("3 / 6");
    expect(depthLabel({ depth: 0, final_depth: 6 })).toBe("— / 6");
  });

  it("says where a cancellation stands", () => {
    expect(cancelNote({ state: "broadcast", cancel_requested_at: 10 })).toEqual(
      {
        key: "requested",
      }
    );
    expect(
      cancelNote({ state: "cancelled", cancel_reason: "sent_by" })
    ).toEqual({ key: "cancelled", reason: "sent_by" });
    expect(cancelNote({ state: "final" })).toBeNull();
  });

  it("flags a queued payout past sent_by, not at it", () => {
    expect(overdue({ state: "queued", sent_by: 100 }, 101)).toBe(true);
    expect(overdue({ state: "queued", sent_by: 100 }, 100)).toBe(false);
    expect(overdue({ state: "broadcast", sent_by: 100 }, 200)).toBe(false);
  });

  it("maps every state to a badge", () => {
    expect(
      (["queued", "broadcast", "confirmed", "final", "cancelled"] as const).map(
        stateVariant
      )
    ).toEqual(["secondary", "outline", "outline", "positive", "warning"]);
  });

  it("formats fee rates and amounts", () => {
    expect(feeRate({ fee_rate_sat_kvb: 2500 })).toBe("2.5 loki/vB");
    expect(feeRate({ fee_rate_sat_kvb: 1000 })).toBe("1 loki/vB");
    expect(feeRate({})).toBeNull();
    expect(loki(19258000)).toBe("19258 loki");
    expect(shorten("short")).toBe("short");
  });
});
