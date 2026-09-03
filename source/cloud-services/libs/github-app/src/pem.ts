import { decodeBase64Url } from "./base64.js";

// PEM is the envelope a private key arrives in, and DER is what WebCrypto
// imports. Only the body is base64.
const PEM_BODY = /-----BEGIN [A-Z ]+-----([\s\S]+?)-----END [A-Z ]+-----/;

/**
 * Reads a PKCS#8 private key.
 *
 * PKCS#8, and not PKCS#1. GitHub issues an App's private key as PKCS#1 — the
 * envelope says `BEGIN RSA PRIVATE KEY` — and `crypto.subtle.importKey`
 * accepts only PKCS#8, whose envelope says `BEGIN PRIVATE KEY`. The conversion
 * is one command against the file GitHub hands you:
 *
 *     openssl pkcs8 -topk8 -nocrypt -in app.pem -out app.pkcs8.pem
 *
 * It is done once, out of band, and the PKCS#8 form is what the secret holds.
 * Converting at runtime would mean parsing ASN.1 and re-wrapping it here, in
 * the one place in this repository that handles a private key — for a
 * transformation that has no reason to happen more than once in the key's life.
 * So a PKCS#1 key is refused with the command that fixes it rather than
 * silently accepted and failing later as an opaque import error.
 */
export async function importSigningKey(pem: string): Promise<CryptoKey> {
  if (pem.includes("BEGIN RSA PRIVATE KEY")) {
    throw new Error(
      "the private key is PKCS#1 and WebCrypto imports only PKCS#8; convert it with " +
        "`openssl pkcs8 -topk8 -nocrypt -in app.pem -out app.pkcs8.pem` and store that",
    );
  }
  const body = PEM_BODY.exec(pem);
  if (!body?.[1]) {
    throw new Error("the private key is not PEM: no BEGIN/END envelope");
  }
  return crypto.subtle.importKey(
    "pkcs8",
    decodeBase64Url(body[1].replaceAll(/\s+/g, "")) as unknown as ArrayBuffer,
    { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
    false,
    ["sign"],
  );
}
