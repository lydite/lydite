import { describe, expect, it } from "vitest";

import { LOGIN_COOKIE, parseLogin, readCookie, REFRESH_COOKIE, refreshCookie } from "./cookies.js";

describe("the sign-in's cookies", () => {
  it("reads one cookie out of several", () => {
    const request = new Request("https://app.lydite.org/", {
      headers: { cookie: `a=1; ${LOGIN_COOKIE}=s.v; b=2` },
    });
    expect(readCookie(request, LOGIN_COOKIE)).toBe("s.v");
    expect(readCookie(request, "missing")).toBeUndefined();
  });

  it("parses a login value only when it is exactly two base64url strings", () => {
    expect(parseLogin("s_1.v-2")).toEqual({ state: "s_1", verifier: "v-2" });
    expect(parseLogin(undefined)).toBeUndefined();
    expect(parseLogin("only")).toBeUndefined();
    expect(parseLogin("a.b.c")).toBeUndefined();
    expect(parseLogin(".v")).toBeUndefined();
    expect(parseLogin("s=.v")).toBeUndefined();
  });

  it("writes no refresh cookie for a token a cookie value cannot hold, or a lifetime that is not positive", () => {
    expect(refreshCookie("ghr ;x", 60)).toBeUndefined();
    expect(refreshCookie("ghr_x", 0)).toBeUndefined();
    expect(refreshCookie("ghr_x", 1.5)).toBeUndefined();
    expect(refreshCookie("ghr_x", 60)).toBe(
      `${REFRESH_COOKIE}=ghr_x; Secure; Path=/; HttpOnly; SameSite=Strict; Max-Age=60`,
    );
  });
});
