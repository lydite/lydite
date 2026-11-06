/**
 * The sign-in's randomness and its PKCE challenge, built on WebCrypto alone so
 * the same code runs in a Worker and under Node.
 */

/** 32 bytes: a state and a verifier each carry 256 bits, well past the 128 a guess has to beat. */
const RANDOM_BYTES = 32;

/**
 * A fresh random value, base64url-encoded without padding. At 32 bytes it is
 * 43 characters of RFC 7636's unreserved set, the shortest a PKCE verifier may be.
 */
export function randomToken(): string {
  return base64url(globalThis.crypto.getRandomValues(new Uint8Array(RANDOM_BYTES)));
}

/** The S256 challenge for a verifier: base64url(SHA-256(verifier)). */
export async function challengeFor(verifier: string): Promise<string> {
  const digest = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  return base64url(new Uint8Array(digest));
}

/**
 * Whether two strings are equal, taking the same time wherever they first
 * differ. A comparison that returns at the first mismatch tells a caller how
 * many leading characters of a state it has right, one attempt at a time.
 * Only the lengths are compared early, and a length is not a secret.
 */
export function sameText(a: string, b: string): boolean {
  const left = new TextEncoder().encode(a);
  const right = new TextEncoder().encode(b);
  if (left.length !== right.length) {
    return false;
  }
  let difference = 0;
  for (let i = 0; i < left.length; i++) {
    difference |= (left[i] ?? 0) ^ (right[i] ?? 0);
  }
  return difference === 0;
}

export function base64url(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
