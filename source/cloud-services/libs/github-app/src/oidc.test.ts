import { describe, expect, it } from "vitest";

import { ACTIONS_ISSUER, pullRequestFromRef, verifyActionsToken } from "./oidc.js";
import { issuerKeys } from "./testing.js";

const AUDIENCE = "https://pr.lydite.org";

function claims(overrides: Record<string, unknown> = {}) {
  return {
    iss: ACTIONS_ISSUER,
    aud: AUDIENCE,
    exp: Math.floor(Date.now() / 1000) + 300,
    repository: "lydite/proving-ground",
    ref: "refs/pull/7/merge",
    ...overrides,
  };
}

describe("verifyActionsToken", () => {
  it("accepts a token GitHub Actions signed for this audience", async () => {
    const keys = await issuerKeys();
    const verified = await verifyActionsToken(await keys.sign(claims()), AUDIENCE, keys.jwks);
    expect(verified.repository).toBe("lydite/proving-ground");
    expect(verified.ref).toBe("refs/pull/7/merge");
  });

  it("refuses a token that is not a JWT", async () => {
    const keys = await issuerKeys();
    await expect(verifyActionsToken("not-a-token", AUDIENCE, keys.jwks)).rejects.toThrow();
  });

  it("refuses an expired token, so one captured from an old run cannot be reused", async () => {
    const keys = await issuerKeys();
    const token = await keys.sign(claims({ exp: Math.floor(Date.now() / 1000) - 1 }));
    await expect(verifyActionsToken(token, AUDIENCE, keys.jwks)).rejects.toThrow(/expired/);
  });

  it("refuses a token minted for another audience", async () => {
    const keys = await issuerKeys();
    const token = await keys.sign(claims({ aud: "https://someone-elses-service" }));
    await expect(verifyActionsToken(token, AUDIENCE, keys.jwks)).rejects.toThrow(/audience/);
  });

  it("refuses a token from another issuer", async () => {
    const keys = await issuerKeys();
    const token = await keys.sign(claims({ iss: "https://token.actions.example.test" }));
    await expect(verifyActionsToken(token, AUDIENCE, keys.jwks)).rejects.toThrow(/GitHub Actions/);
  });

  // A signature checked against a key the issuer does not publish is not
  // checked at all.
  it("refuses a token signed by a key the issuer does not publish", async () => {
    const mine = await issuerKeys("attacker-key");
    const theirs = await issuerKeys("real-key");
    await expect(verifyActionsToken(await mine.sign(claims()), AUDIENCE, theirs.jwks)).rejects.toThrow();
  });

  // The claims are the payload, so a payload swapped under a real signature is
  // the attack the signature exists to stop.
  it("refuses a token whose claims were edited after signing", async () => {
    const keys = await issuerKeys();
    const token = await keys.sign(claims());
    const [header, , signature] = token.split(".");
    const forged = btoa(JSON.stringify(claims({ repository: "someone/else" })))
      .replaceAll("+", "-")
      .replaceAll("/", "_")
      .replaceAll("=", "");
    await expect(
      verifyActionsToken(`${header}.${forged}.${signature}`, AUDIENCE, keys.jwks),
    ).rejects.toThrow(/signature/);
  });

  // Honouring the token's own choice of algorithm is how a verifier ends up
  // accepting `alg: none`.
  it("refuses a token that declares an algorithm other than RS256", async () => {
    const keys = await issuerKeys();
    const token = await keys.sign(claims(), { alg: "none" });
    await expect(verifyActionsToken(token, AUDIENCE, keys.jwks)).rejects.toThrow(/RS256/);
  });
});

describe("pullRequestFromRef", () => {
  it("reads the number a merge run was triggered for", () => {
    expect(pullRequestFromRef("refs/pull/42/merge")).toBe(42);
  });

  // A push build has no pull request, and must not be able to name one.
  it("yields nothing for a ref that is not a pull request's", () => {
    for (const ref of ["refs/heads/main", "refs/tags/v1.0.0", "refs/pull/x/merge", ""]) {
      expect(pullRequestFromRef(ref)).toBeUndefined();
    }
  });
});
