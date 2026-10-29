import type { CashWalletClaim } from "src/types";

// An owner lock is a live, unclaimed slice its Hub's owner can neither remove
// nor delete the bill of (NIP-CASH §Data Model): one minted for an order
// ("ordered", it never expires) or re-minted by a cancelled on-chain
// withdrawal ("reminted"). Only its holder ends it.
export type OwnerLock = "ordered" | "reminted";

type LockFields = Pick<
  CashWalletClaim,
  "ordered" | "reminted" | "claimed" | "archived"
>;

export function ownerLock(c: LockFields): OwnerLock | null {
  if (c.claimed || c.archived) {
    return null;
  }
  if (c.reminted) {
    return "reminted";
  }
  if (c.ordered) {
    return "ordered";
  }
  return null;
}

// groupLock is the lock that keeps a bill from being deleted: any of its
// slices locked.
export function groupLock(claims: LockFields[]): OwnerLock | null {
  for (const c of claims) {
    const l = ownerLock(c);
    if (l) {
      return l;
    }
  }
  return null;
}
