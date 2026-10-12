import { expect, it } from "vitest";
import { loginReturnPath } from "../../web/return-path";
it("accepts only canonical same-origin admin destinations", () => {
  for (const target of ["/admin", "/admin/users/user-id", "/admin/audit?actor=a#event", "/api/identity/v1/authorize?client_id=studio&state=abc"])
    expect(loginReturnPath(target, "https://app.example")).toBe(target);
  for (const target of [null, "https://app.example/admin", "https://evil/admin", "//evil/admin", "/administrator", "/admin/../logout", "/admin/%2e%2e/logout", "/admin/%2f%2fevil", "/admin/\\evil", "/admin//evil", "/admin\n", "/api/identity/v1/token", "/api/identity/v1/authorize/", "/api/identity/v1/../v1/authorize"]) expect(loginReturnPath(target, "https://app.example")).toBeNull();
});
