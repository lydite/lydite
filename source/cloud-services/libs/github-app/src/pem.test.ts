import { describe, expect, it } from "vitest";

import { importSigningKey } from "./pem.js";
import { issuerKeys } from "./testing.js";

describe("importSigningKey", () => {
  it("imports a PKCS#8 key", async () => {
    const keys = await issuerKeys();
    await expect(importSigningKey(await keys.privateKeyPkcs8Pem())).resolves.toBeDefined();
  });

  // GitHub issues an App's key as PKCS#1 and WebCrypto imports only PKCS#8, so
  // this is the mistake every first deployment makes. An opaque import failure
  // would send somebody looking at their key's contents; the fix belongs in the
  // message.
  it("refuses a PKCS#1 key and names the command that converts it", async () => {
    // The envelope is the whole of the fixture: the body is a dozen characters
    // of nothing, because this asserts that a PKCS#1 header is refused before
    // anything is parsed. A Biome suppression has to be the line immediately
    // above what it suppresses, so the reason goes here and the directive last.
    // biome-ignore lint/security/noSecrets: a PEM envelope wrapping no key
    const pkcs1 = "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJB\n-----END RSA PRIVATE KEY-----\n";
    await expect(importSigningKey(pkcs1)).rejects.toThrow(/openssl pkcs8 -topk8/);
  });

  it("refuses something that is not PEM at all", async () => {
    await expect(importSigningKey("hunter2")).rejects.toThrow(/not PEM/);
  });
});
