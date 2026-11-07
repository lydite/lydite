import { describe, expect, it } from "vitest";

import { challengeFor, randomToken, sameText } from "./pkce.js";

describe("the sign-in's randomness and PKCE challenge", () => {
  it("draws a 43-character base64url token, the shortest verifier RFC 7636 allows", () => {
    expect(randomToken()).toMatch(/^[A-Za-z0-9_-]{43}$/);
  });

  // The expected value is `openssl dgst -sha256 -binary | base64` with the
  // base64url substitutions applied; it carries both `-` and `_`, so a challenge
  // left in standard base64 would not match.
  it("derives the S256 challenge as unpadded base64url of the verifier's SHA-256", async () => {
    // biome-ignore lint/security/noSecrets: a known SHA-256 digest of "lydite", not a credential
    expect(await challengeFor("lydite")).toBe("Idmvo_m_hkCGNp_s--itOkr6Ld3JOVkgZdFZjIZGW1M");
  });

  it("compares equal strings as equal", () => {
    expect(sameText("abc", "abc")).toBe(true);
  });

  it("compares strings differing in content or length as unequal", () => {
    expect(sameText("abc", "abd")).toBe(false);
    expect(sameText("abc", "abcd")).toBe(false);
    expect(sameText("", "a")).toBe(false);
  });
});
