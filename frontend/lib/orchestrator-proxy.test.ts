import { describe, expect, it } from "vitest";

import { isCrossOriginWrite } from "./orchestrator-proxy";

const SELF = "https://workbench.example.com";

function req(method: string, origin?: string) {
  return new Request(`${SELF}/api/sessions/abc`, {
    method,
    headers: origin ? { origin } : {},
    body: method === "GET" || method === "HEAD" ? undefined : ""
  });
}

// What the route actually receives in production: nginx terminates TLS and
// proxies to 127.0.0.1:9001, so request.url is the internal address and the
// public host arrives only in X-Forwarded-Host.
function proxiedReq(method: string, origin: string, forwardedHost: string) {
  return new Request(`http://127.0.0.1:9001/api/sessions/abc`, {
    method,
    headers: { origin, "x-forwarded-host": forwardedHost },
    body: ""
  });
}

// The app ships frame-ancestors * and a SameSite=None cookie so third-party
// portals can embed it, which means the browser attaches the session cookie to
// requests started by ANY page. This check is what replaced SameSite as the CSRF
// defense, so each branch is pinned rather than assumed.
describe("isCrossOriginWrite", () => {
  it("allows same-origin writes from the embedded app", () => {
    for (const method of ["POST", "PUT", "PATCH", "DELETE"]) {
      expect(isCrossOriginWrite(req(method, SELF), new URL(`${SELF}/`))).toBe(false);
    }
  });

  it("refuses cross-origin writes", () => {
    for (const method of ["POST", "PUT", "PATCH", "DELETE"]) {
      const request = req(method, "https://evil.example.com");
      expect(isCrossOriginWrite(request, new URL(`${SELF}/`))).toBe(true);
    }
  });

  // Reads are safe to serve cross-origin: that is what makes the embed work at
  // all, and a GET cannot change state.
  it("allows cross-origin reads", () => {
    expect(isCrossOriginWrite(req("GET", "https://evil.example.com"), new URL(`${SELF}/`))).toBe(
      false
    );
    expect(isCrossOriginWrite(req("HEAD", "https://evil.example.com"), new URL(`${SELF}/`))).toBe(
      false
    );
  });

  // Browsers always send Origin on cross-origin non-GET, so no Origin means a
  // non-browser client with no ambient cookie — not a CSRF vector.
  it("allows writes with no Origin header (non-browser clients)", () => {
    expect(isCrossOriginWrite(req("POST"), new URL(`${SELF}/`))).toBe(false);
  });

  // A same host on a different port is still a different origin to the browser.
  it("treats a different port as cross-origin", () => {
    const request = req("POST", "https://workbench.example.com:8443");
    expect(isCrossOriginWrite(request, new URL(`${SELF}/`))).toBe(true);
  });

  it("refuses an unparseable Origin", () => {
    const request = req("POST", "not-a-url");
    expect(isCrossOriginWrite(request, new URL(`${SELF}/`))).toBe(true);
  });

  // Regression: comparing Origin against request.url instead of X-Forwarded-Host
  // 403s every write in production, because behind nginx request.url is always
  // the internal 127.0.0.1:9001 address.
  describe("behind a TLS-terminating proxy", () => {
    const internal = new URL("http://127.0.0.1:9001/api/sessions/abc");

    it("allows a write from the public origin", () => {
      const request = proxiedReq("POST", "https://workbench.example.com", "workbench.example.com");
      expect(isCrossOriginWrite(request, internal)).toBe(false);
    });

    it("still refuses a cross-origin write", () => {
      const request = proxiedReq("POST", "https://evil.example.com", "workbench.example.com");
      expect(isCrossOriginWrite(request, internal)).toBe(true);
    });
  });
});
