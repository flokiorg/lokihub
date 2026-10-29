import type {
  ErrorResponse,
  LSPAcceptedTerm,
  LSPTermField,
  LSPTerms,
} from "src/types";

// The order screens' "announced vs live" check (NIP-LSP live check, WP-26):
// live terms worse than the LSP announced are shown next to the announced
// ones and the user is asked again before buying.

// needsConfirmation: live terms worse than announced that the user has not
// accepted yet at exactly these live values.
export function needsConfirmation(
  terms: LSPTerms | undefined | null,
  accepted: LSPAcceptedTerm[] | undefined | null
): boolean {
  if (!terms?.worse) {
    return false;
  }
  const ok = new Set((accepted ?? []).map((a) => `${a.field}\u0000${a.live}`));
  return worseFields(terms).some((f) => !ok.has(`${f.field}\u0000${f.live}`));
}

export function worseFields(terms: LSPTerms): LSPTermField[] {
  return terms.fields.filter((f) => f.verdict === "worse");
}

// changedFields are the values shown: those that differ, worse first.
export function changedFields(terms: LSPTerms): LSPTermField[] {
  const changed = terms.fields.filter((f) => f.verdict !== "same");
  return [
    ...changed.filter((f) => f.verdict === "worse"),
    ...changed.filter((f) => f.verdict !== "worse"),
  ];
}

// acceptedTerms is what the purchase sends once the user confirms: every
// worse value at its live value.
export function acceptedTerms(terms: LSPTerms): LSPAcceptedTerm[] {
  return worseFields(terms).map((f) => ({ field: f.field, live: f.live }));
}

// termsFromError reads the terms of a purchase refused (409) for live terms
// worse than announced, so the user can be asked again.
export function termsFromError(e: unknown): LSPTerms | null {
  const body = (e as { body?: ErrorResponse } | null)?.body;
  const terms = body?.terms;
  if (!terms || !Array.isArray(terms.fields) || !terms.worse) {
    return null;
  }
  return terms;
}

// fieldName is the field without the LSPS2 row prefix: the order screens buy
// the first fee row only.
export function fieldName(field: string): string {
  return field.replace(/^lsps2_fee\[\d+\]\./, "");
}

// fieldUnit is the unit a value is shown in: LSPS2 amounts in mloki, LSPS1
// balances in loki, ppm, blocks or none.
export function fieldUnit(field: string): string {
  const name = fieldName(field);
  const lsps2 = field.startsWith("lsps2_fee[");
  if (name === "proportional") {
    return "ppm";
  }
  if (/(fee|size)$/.test(name) && lsps2) {
    return "mloki";
  }
  if (/balance$/.test(name)) {
    return "loki";
  }
  if (/(blocks|lifetime|delay)$/.test(name)) {
    return "blocks";
  }
  return "";
}

// formatValue shows a value with its unit; an absent one as an em dash.
export function formatValue(field: string, value: string): string {
  if (value === "") {
    return "—";
  }
  const unit = fieldUnit(field);
  return unit ? `${value} ${unit}` : value;
}

// withoutTerms is the fee params as the buy sends them back.
export function withoutTerms<T extends { terms?: LSPTerms }>(
  v: T
): Omit<T, "terms"> {
  const rest: Partial<T> = { ...v };
  delete rest.terms;
  return rest as Omit<T, "terms">;
}
