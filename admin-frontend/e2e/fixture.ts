import type { Page } from "@playwright/test";
import type { Group, Mutation, User } from "../lib/models";
export async function adminFixture(page: Page, role: "admin" | "user" = "admin") {
  const group: Group = { id: "standard-users", name: "Standard users", revision: 1 };
  const user = (id: string, username: string, role: "admin" | "user" = "user"): User => ({ id, username, display_name: username, role, status: "active", revision: 1, session_revision: 1, must_change_password: false, created_at: "2026-10-11T00:00:00Z", updated_at: "2026-10-11T00:00:00Z", groups: [group] });
  const identity = user("admin-id", "administrator", role);
  const users = [identity, user("user-id", "ordinary")];
  const calls: { path: string; method: string; body: Record<string, unknown> | null }[] = [];
  const receipts = new Map<string, Mutation>();
  const controls = { loseCreateReply: false, conflictEdit: false };
  await page.setExtraHTTPHeaders({ "X-Project-Account-ID": identity.id });
  await page.route("**/api/**", async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method();
    let body: Record<string, unknown> | null = null; try { body = request.postDataJSON(); } catch {}
    calls.push({ path, method, body });
    if (path === "/api/auth/session") return route.fulfill({ json: identity });
    if (path === "/api/me/preferences") return route.fulfill({ json: { revision: 1, theme_mode: "system", accent: "neutral" } });
    if (path === "/api/auth/logout") return route.fulfill({ status: 204 });
    if (path.startsWith("/api/admin/") && role !== "admin") return route.fulfill({ status: 403, json: { code: "action_denied", error: "administrator role required" } });
    if (path === "/api/admin/groups") return route.fulfill({ json: { groups: [group] } });
    if (path === "/api/admin/users" && method === "GET") {
      const filtered = users.filter(user => (!url.searchParams.get("search") || `${user.username} ${user.display_name}`.includes(url.searchParams.get("search")!)) && (!url.searchParams.get("role") || user.role === url.searchParams.get("role")) && (!url.searchParams.get("status") || user.status === url.searchParams.get("status")));
      return route.fulfill({ json: { users: filtered } });
    }
    if (path === "/api/admin/audit-events") return route.fulfill({ json: { events: [{ id: 1, actor_id: identity.id, action: "account.create", target_kind: "account", target_id: "user-id", request_id: "create-fixture", revision: 1, created_at: "2026-10-11T00:00:00Z", details: { after: { display_name: "ordinary", role: "user" } } }] } });
    if (path.startsWith("/api/admin/operations/")) return route.fulfill({ json: { id: "operation", actor_id: identity.id, action: "account.suspend", target_kind: "account", target_id: "user-id", state: "accepted", total_targets: 2, completed_targets: 0, created_at: "2026-10-11T00:00:00Z", updated_at: "2026-10-11T00:00:00Z" } });
    if (body?.request_id && receipts.has(String(body.request_id))) return route.fulfill({ json: { ...receipts.get(String(body.request_id)), replayed: true } });
    if (path === "/api/admin/users" && method === "POST") {
      const created = user("created-id", String(body!.username), body!.role as "admin" | "user"); created.display_name = String(body!.display_name); created.must_change_password = true;
      if (Array.isArray(body?.group_ids)) created.groups = body.group_ids.length ? [group] : [];
      users.push(created); const result = { user: created, replayed: false }; receipts.set(String(body!.request_id), result);
      if (controls.loseCreateReply) { controls.loseCreateReply = false; return route.abort("failed"); }
      return route.fulfill({ status: 201, json: { ...result, temporary_password: "temporary-password-value" } });
    }
    const id = path.split("/")[4]; const target = users.find(user => user.id === id);
    if (!target) return route.fulfill({ status: 404, json: { error: "not found" } });
    if (method === "GET") return route.fulfill({ json: target });
    if (method === "PATCH") {
      if (controls.conflictEdit) { controls.conflictEdit = false; target.role = "admin"; target.revision++; return route.fulfill({ status: 409, json: { code: "revision_conflict", error: "changed" } }); }
      if (body!.expected_revision !== target.revision) return route.fulfill({ status: 409, json: { code: "revision_conflict", error: "changed" } });
      if (body?.display_name) target.display_name = String(body.display_name); if (body?.role) target.role = body.role as "user" | "admin"; target.revision++;
      const result = { user: { ...target }, replayed: false }; receipts.set(String(body!.request_id), result); return route.fulfill({ json: result });
    }
    if (path.endsWith("/password-reset")) { target.must_change_password = true; target.revision++; const result = { user: { ...target }, replayed: false }; receipts.set(String(body!.request_id), result); return route.fulfill({ json: { ...result, temporary_password: "temporary-password-value" } }); }
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  return { calls, users, controls };
}
