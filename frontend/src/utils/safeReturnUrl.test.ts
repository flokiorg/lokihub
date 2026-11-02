import { describe, expect, it } from "vitest";
import { safeReturnUrl } from "./safeReturnUrl";

describe("safeReturnUrl", () => {
  it("keeps a link to another app or site", () => {
    expect(safeReturnUrl("https://example.com/cb?state=1")).toBe(
      "https://example.com/cb?state=1"
    );
    expect(safeReturnUrl("myapp://nwc?pubkey=ab")).toBe(
      "myapp://nwc?pubkey=ab"
    );
  });

  // PaymentSuccess routes an LNURL successAction.url (set by the payee's
  // LNURL-pay server, untrusted) through this guard before it can become an
  // ExternalLink / window.open target (LOKIHUB-0040).
  it("rejects a javascript: LNURL successAction url and keeps an https one", () => {
    expect(safeReturnUrl("javascript:alert(document.cookie)")).toBeUndefined();
    expect(
      safeReturnUrl("data:text/html,<script>alert(1)</script>")
    ).toBeUndefined();
    expect(safeReturnUrl("https://payee.example/receipt?x=1")).toBe(
      "https://payee.example/receipt?x=1"
    );
  });

  it("refuses anything that runs in this app's page", () => {
    for (const bad of [
      "javascript:alert(1)",
      "JavaScript:alert(1)",
      "java\tscript:alert(1)",
      " javascript:alert(1)",
      "vbscript:msgbox(1)",
      "data:text/html,<script>alert(1)</script>",
      "blob:https://example.com/0",
      "file:///etc/passwd",
      "about:blank",
      "/peers",
      "",
      null,
      undefined,
    ]) {
      expect(safeReturnUrl(bad as string | null | undefined)).toBeUndefined();
    }
  });
});
