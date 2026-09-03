import { decodeBase64Url } from "./base64.js";

/** GitHub Actions' OIDC issuer. The only one whose tokens this accepts. */
export const ACTIONS_ISSUER = "https://token.actions.githubusercontent.com";

/**
 * The claims the relay acts on.
 *
 * `repository` and `ref` are the load-bearing pair. The repository a comment is
 * posted to is taken from the claim and never from the request body, so a job
 * cannot ask for a comment on a repository it does not belong to; `ref` is what
 * the submitted pull-request number is checked against, so it cannot ask for
 * one on a different pull request either.
 */
export interface ActionsClaims {
  iss: string;
  aud: string;
  exp: number;
  repository: string;
  ref: string;
  sha?: string;
  workflow_ref?: string;
}

interface Jwk {
  kid: string;
  kty: string;
  alg?: string;
  n: string;
  e: string;
}

/**
 * Verifies a GitHub Actions OIDC token and returns its claims.
 *
 * This is the whole of the trust. A CI job holds no credential — it holds a
 * token GitHub minted for it, scoped to that run — so everything the relay
 * believes about who is asking comes from a signature it checked against
 * GitHub's own keys, and from claims the requester cannot set.
 *
 * Four things are checked, and each closes a distinct hole: the signature, so
 * the token is GitHub's; `iss`, so it is Actions' and not another issuer's; a
 * declared `aud`, so a token minted for some other service cannot be replayed
 * here; and `exp`, so one captured from an old run cannot be reused. Anything
 * that fails is a rejection with no detail — a verifier that explains which
 * check failed is an oracle.
 */
export async function verifyActionsToken(
  token: string,
  audience: string,
  fetchJwks: () => Promise<{ keys: Jwk[] }>,
  now: () => number = () => Math.floor(Date.now() / 1000),
): Promise<ActionsClaims> {
  const parts = token.split(".");
  if (parts.length !== 3) {
    throw new Error("the token is not a JWT");
  }
  const [rawHeader, rawPayload, rawSignature] = parts as [string, string, string];

  const header = parseJson(rawHeader) as { alg?: string; kid?: string };
  // RS256 and nothing else. Accepting the token's own choice of algorithm is
  // how a verifier ends up honouring `alg: none`, or verifying an RSA public
  // key as an HMAC secret.
  if (header.alg !== "RS256" || !header.kid) {
    throw new Error("the token is not signed with RS256 by a named key");
  }

  const { keys } = await fetchJwks();
  const jwk = keys.find((candidate) => candidate.kid === header.kid);
  if (!jwk) {
    throw new Error("the token names a key the issuer does not publish");
  }
  const key = await crypto.subtle.importKey(
    "jwk",
    { kty: jwk.kty, n: jwk.n, e: jwk.e, alg: "RS256", ext: true },
    { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
    false,
    ["verify"],
  );
  const signed = new TextEncoder().encode(`${rawHeader}.${rawPayload}`);
  const ok = await crypto.subtle.verify(
    "RSASSA-PKCS1-v1_5",
    key,
    decodeBase64Url(rawSignature) as unknown as ArrayBuffer,
    signed as unknown as ArrayBuffer,
  );
  if (!ok) {
    throw new Error("the token's signature does not verify");
  }

  const claims = parseJson(rawPayload) as ActionsClaims;
  if (claims.iss !== ACTIONS_ISSUER) {
    throw new Error("the token was not issued by GitHub Actions");
  }
  if (claims.aud !== audience) {
    throw new Error("the token was minted for another audience");
  }
  if (!claims.exp || claims.exp <= now()) {
    throw new Error("the token has expired");
  }
  if (!claims.repository) {
    throw new Error("the token names no repository");
  }
  return claims;
}

/**
 * Reads the pull-request number out of the ref a merge run was triggered for.
 *
 * `refs/pull/<n>/merge` is what `pull_request` events check out, and it is the
 * only statement of which pull request a run belongs to that the run itself
 * cannot choose. A ref of any other shape yields nothing, which is what makes
 * a push build unable to comment.
 */
export function pullRequestFromRef(ref: string): number | undefined {
  const match = /^refs\/pull\/(\d+)\/(merge|head)$/.exec(ref);
  if (!match?.[1]) {
    return undefined;
  }
  return Number(match[1]);
}

function parseJson(part: string): unknown {
  return JSON.parse(new TextDecoder().decode(decodeBase64Url(part)));
}
