import { describe, expect, it } from "vitest";
import { termsFromError } from "src/components/lsps/terms";
import { errorBody } from "src/lib/errorBody";

const terms = {
  worse: true,
  fields: [
    {
      field: "lsps2_fee[0].min_fee",
      announced: "2000000",
      live: "3000000",
      verdict: "worse" as const,
    },
  ],
};

// The terms-changed refusal reaches the order screens alike over HTTP (409
// body) and in the desktop app (router response body).
describe("errorBody", () => {
  it("reads a refusal's payload on both transports", () => {
    const refusal = { message: "worse than it announced", terms };
    const http = { body: errorBody(refusal) };
    const desktop = { body: errorBody(refusal) };
    expect(termsFromError(http)).toEqual(terms);
    expect(termsFromError(desktop)).toEqual(terms);
  });

  it("reads no payload from a plain refusal", () => {
    expect(errorBody(null)).toBeUndefined();
    expect(errorBody(undefined)).toBeUndefined();
    expect(errorBody("text")).toBeUndefined();
    expect(errorBody({ terms })).toBeUndefined();
    expect(termsFromError({ body: errorBody({ message: "no" }) })).toBeNull();
  });
});
