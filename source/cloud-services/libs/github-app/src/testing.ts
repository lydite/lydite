import { encodeBase64Url, encodeJson } from "./base64.js";

/**
 * A throwaway RSA key pair, and the JWKS that publishes its public half.
 *
 * Real keys and real signatures, generated per test run. A stub that returns
 * "valid" for a token shaped correctly tests the shape of the code rather than
 * the verification, and the verification is the entire trust boundary.
 */
export async function issuerKeys(kid = "test-key"): Promise<{
  sign: (claims: Record<string, unknown>, header?: Record<string, unknown>) => Promise<string>;
  jwks: () => Promise<{ keys: never[] }>;
  privateKeyPkcs8Pem: () => Promise<string>;
}> {
  const pair = await crypto.subtle.generateKey(
    { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
    true,
    ["sign", "verify"],
  );
  const publicJwk = await crypto.subtle.exportKey("jwk", pair.publicKey);

  return {
    async sign(claims, header = {}) {
      const head = encodeJson({ alg: "RS256", typ: "JWT", kid, ...header });
      const payload = encodeJson(claims);
      const signature = await crypto.subtle.sign(
        "RSASSA-PKCS1-v1_5",
        pair.privateKey,
        new TextEncoder().encode(`${head}.${payload}`) as unknown as ArrayBuffer,
      );
      return `${head}.${payload}.${encodeBase64Url(signature)}`;
    },
    async jwks() {
      return {
        keys: [{ kid, kty: publicJwk.kty, n: publicJwk.n, e: publicJwk.e, alg: "RS256" }],
      } as unknown as { keys: never[] };
    },
    async privateKeyPkcs8Pem() {
      const der = await crypto.subtle.exportKey("pkcs8", pair.privateKey);
      const body = btoa(String.fromCharCode(...new Uint8Array(der))).replace(/(.{64})/g, "$1\n");
      return `-----BEGIN PRIVATE KEY-----\n${body}\n-----END PRIVATE KEY-----\n`;
    },
  };
}
