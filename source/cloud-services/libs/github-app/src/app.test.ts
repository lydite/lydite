import { describe, expect, it } from "vitest";

import { appJwt, installationId, installationToken } from "./app.js";
import { decodeBase64Url } from "./base64.js";
import { issuerKeys } from "./testing.js";

function payloadOf(jwt: string): Record<string, number | string> {
  const part = jwt.split(".")[1] as string;
  return JSON.parse(new TextDecoder().decode(decodeBase64Url(part)));
}

describe("appJwt", () => {
  it("names the App as the issuer and stays inside GitHub's ten-minute ceiling", async () => {
    const keys = await issuerKeys();
    const claims = payloadOf(await appJwt("1234", await keys.privateKeyPkcs8Pem()));
    expect(claims.iss).toBe("1234");
    expect(Number(claims.exp) - Number(claims.iat)).toBeLessThanOrEqual(600);
  });

  // GitHub refuses a token whose iat is in the future, and a Worker's clock is
  // not GitHub's clock.
  it("backdates iat so a clock a second fast is not a rejection", async () => {
    const keys = await issuerKeys();
    const now = Date.now();
    const claims = payloadOf(await appJwt("1234", await keys.privateKeyPkcs8Pem(), now));
    expect(Number(claims.iat)).toBeLessThan(Math.floor(now / 1000));
  });
});

describe("installationId", () => {
  // The ordinary state for every repository that has not opted in. Treating it
  // as a failure is what would turn "not installed" into a broken run instead
  // of a fallback.
  it("answers nothing for a repository the app is not installed on", async () => {
    const found = await installationId("jwt", "someone/else", async () => new Response("", { status: 404 }));
    expect(found).toBeUndefined();
  });

  it("reads the installation id", async () => {
    const found = await installationId("jwt", "lydite/lydite", async () =>
      Response.json({ id: 987 }),
    );
    expect(found).toBe(987);
  });

  it("fails loudly on any other error, which is not an answer", async () => {
    await expect(
      installationId("jwt", "lydite/lydite", async () => new Response("", { status: 500 })),
    ).rejects.toThrow(/500/);
  });
});

describe("installationToken", () => {
  // Both narrowings are the point: the token is minted for one repository and
  // one permission, so the worst a compromised relay request can do is write a
  // comment.
  it("asks for one repository and pull-request write and nothing else", async () => {
    let sent: Record<string, unknown> = {};
    await installationToken("jwt", 1, "lydite/proving-ground", async (_url, init) => {
      sent = JSON.parse(String(init?.body));
      return Response.json({ token: "ghs_x" });
    });
    expect(sent.repositories).toEqual(["proving-ground"]);
    expect(sent.permissions).toEqual({ pull_requests: "write" });
  });
});
