import type { Page } from "@playwright/test";
import { productActions } from "../../web/capabilities";
import type { GroupDetail, Policy, PolicyResult } from "../lib/policy-models";
import type { Group, Mutation, User } from "../lib/models";
export async function adminFixture(page: Page, role: "admin" | "user" = "admin") {
  const group: Group = { id: "standard-users", name: "Standard users", revision: 1 };
  const user = (id: string, username: string, role: "admin" | "user" = "user"): User => ({ id, username, display_name: username, role, status: "active", revision: 1, session_revision: 1, must_change_password: false, created_at: "2026-10-11T00:00:00Z", updated_at: "2026-10-11T00:00:00Z", groups: [group] });
  const identity = user("admin-id", "administrator", role);
  const users = [identity, user("user-id", "ordinary")];
  const calls: { path: string; method: string; body: Record<string, unknown> | null }[] = [];
  const receipts = new Map<string, Mutation | PolicyResult>();
  const policies: Policy[] = [{id:"standard-policy",name:"Standard operations",description:"",revision:1,statements:Object.fromEntries(productActions.filter(action=>action!=="admin.content.read").map(action=>[action,"allow"]))}];
  const groups: GroupDetail[] = [{...group,description:"",member_count:2,policy_ids:["standard-policy"]}];
  const memberships = new Map<string,string[]>([[group.id,users.map(user=>user.id)]]);
  const controls = { loseCreateReply: false, conflictEdit: false, losePolicyReply: false, conflictPolicy: false, stalePreview: false };
  await page.setExtraHTTPHeaders({ "X-Project-Account-ID": identity.id });
  await page.route("**/api/**", async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method();
    let body: Record<string, unknown> | null = null; try { body = request.postDataJSON(); } catch {}
    calls.push({ path, method, body });
    if (path === "/api/auth/session") return route.fulfill({ json: identity });
    if (path === "/api/me/capabilities") return route.fulfill({json:{account_id:identity.id,account_status:"active",management_role:identity.role,revision:1,actions:{"admin.content.read":{allowed:false}}}});
    if (path === "/api/me/preferences") return route.fulfill({ json: { revision: 1, theme_mode: "system", accent: "neutral" } });
    if (path === "/api/auth/logout") return route.fulfill({ status: 204 });
    if (path.startsWith("/api/admin/") && role !== "admin") return route.fulfill({ status: 403, json: { code: "action_denied", error: "administrator role required" } });
    if (path.endsWith("/effective-access")) {
      const accountID=path.split("/")[4];
      return route.fulfill({json:{account_id:accountID,account_status:"active",management_role:"user",revision:1,actions:Object.fromEntries(productActions.map(action=>{const sources=groups.filter(group=>memberships.get(group.id)?.includes(accountID)).flatMap(group=>policies.filter(policy=>group.policy_ids.includes(policy.id)&&policy.statements[action]&&policy.statements[action]!=="unset").map(policy=>({group_id:group.id,group_name:group.name,policy_id:policy.id,policy_name:policy.name,effect:policy.statements[action]})));return[action,{allowed:sources.some(source=>source.effect==="allow")&&!sources.some(source=>source.effect==="deny"),sources}];}))}});
    }
    if (path.startsWith("/api/admin/groups") || path.startsWith("/api/admin/policies")) {
      const parts=path.split("/"), kind=parts[3], id=parts[4], sub=parts[5], collection=kind==="groups"?groups:policies;
      const target=collection.find(item=>item.id===id);
      const reply=(value:PolicyResult)=>{receipts.set(String(body!.request_id),structuredClone(value));return route.fulfill({json:value});};
      if (body?.request_id&&receipts.has(String(body.request_id))) return route.fulfill({json:{...receipts.get(String(body.request_id)),replayed:true}});
      if (!id&&method==="GET") return route.fulfill({json:{[kind]:collection}});
      if (!id&&method==="POST") {
        const base={id:`created-${kind}`,name:String(body!.name),description:String(body!.description||""),revision:1};
        if(kind==="groups"){const value={...base,member_count:0,policy_ids:[]};groups.push(value);memberships.set(value.id,[]);return reply({group:value,replayed:false});}
        const value={...base,statements:body!.statements as Policy["statements"]};policies.push(value);
        const result={policy:value,replayed:false};receipts.set(String(body!.request_id),structuredClone(result));
        if(controls.losePolicyReply){controls.losePolicyReply=false;return route.abort("failed");}
        return route.fulfill({json:result});
      }
      if(!target)return route.fulfill({status:404,json:{error:"not found"}});
      if(sub==="members"&&method==="GET")return route.fulfill({json:{users:users.filter(user=>memberships.get(id)?.includes(user.id))}});
      if(sub==="deletion-preview")return route.fulfill({json:{group:target,expected_dependencies:"fingerprint",users:users.filter(user=>memberships.get(id)?.includes(user.id)).map(user=>({account_id:user.id,username:user.username,account_status:user.status,management_role:user.role,before_access:{"agents.run":{allowed:true,sources:[]}},after_access:{"agents.run":{allowed:false,sources:[]}},before_resources:{projects:{default:"5",ceiling:"10",effective:"5"}},after_resources:{projects:{default:"0",ceiling:"0",effective:"0"}}}))}});
      if(method==="GET")return route.fulfill({json:target});
      if(controls.conflictPolicy&&kind==="policies"){controls.conflictPolicy=false;target.description="Concurrent description";target.revision++;return route.fulfill({status:409,json:{code:"revision_conflict",error:"changed"}});}
      if(body!.expected_revision!==target.revision)return route.fulfill({status:409,json:{code:"revision_conflict",error:"changed"}});
      if(method==="DELETE"){
        if(kind==="groups"&&controls.stalePreview){controls.stalePreview=false;return route.fulfill({status:409,json:{code:"preview_stale",error:"changed"}});}
        collection.splice(collection.findIndex(item=>item.id===target.id),1);return reply({deleted_id:id,replayed:false});
      }
      if(sub==="members"&&"member_count"in target){const added=body!.add_user_ids as string[],removed=body!.remove_user_ids as string[];memberships.set(id,[...new Set([...(memberships.get(id)||[]),...added])].filter(value=>!removed.includes(value)));target.member_count=memberships.get(id)!.length;}
      else if(sub==="policies"&&"policy_ids"in target)target.policy_ids=body!.policy_ids as string[];
      else {if(body?.name)target.name=String(body.name);if(body?.description!==undefined)target.description=String(body.description);if(body?.statements&&"statements"in target)target.statements=body.statements as Policy["statements"];}
      target.revision++;return reply({[kind==="groups"?"group":"policy"]:target,replayed:false});
    }
    if (path === "/api/admin/users" && method === "GET") {
      const filtered = users.filter(user => (!url.searchParams.get("search") || `${user.username} ${user.display_name}`.includes(url.searchParams.get("search")!)) && (!url.searchParams.get("role") || user.role === url.searchParams.get("role")) && (!url.searchParams.get("status") || user.status === url.searchParams.get("status")));
      return route.fulfill({ json: { users: filtered } });
    }
    if (path === "/api/admin/audit-events") return route.fulfill({ json: { events: [{ id: 1, actor_id: identity.id, action: "account.create", target_kind: "account", target_id: "user-id", request_id: "create-fixture", revision: 1, created_at: "2026-10-11T00:00:00Z", details: { after: { display_name: "ordinary", role: "user" } } }] } });
    if (path.startsWith("/api/admin/operations/") && path.endsWith("/targets")) return route.fulfill({json:{targets:[{project_id:"project-a",state:"accepted"},{project_id:"project-b",state:"unknown",error:"Engine unavailable"}]}});
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
  return { calls, users, controls, groups, policies, memberships };
}
