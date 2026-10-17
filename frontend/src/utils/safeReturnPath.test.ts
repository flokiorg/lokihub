import { describe, expect, it } from "vitest";
import { safeReturnPath } from "./safeReturnPath";

describe("safeReturnPath", () => {
  it("keeps a plain in-app path, with its query", () => {
    expect(safeReturnPath("/wallet/send?amount=5")).toBe("/wallet/send?amount=5");
    expect(safeReturnPath("/setup/password")).toBe("/setup/password");
  });

  it("refuses anything that could leave the app", () => {
    for (const bad of [
      "//evil.example",
      "/\\evil.example",
      "/\\/evil.example",
      "/foo\\bar",
      "https://evil.example",
      "javascript:alert(1)",
      "evil.example",
      "/\tevil",
      "",
      null,
      undefined,
    ]) {
      expect(safeReturnPath(bad as string | null | undefined)).toBeUndefined();
    }
  });
});
