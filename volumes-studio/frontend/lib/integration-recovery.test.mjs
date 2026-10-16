import test from "node:test";
import assert from "node:assert/strict";
import { readIntegrationCommand, integrationEndpoint } from "./integration-recovery.ts";
const digest = `sha256:${"a".repeat(64)}`;
const record = (changes = {}) => ({ library: "library", branch: "branch", action: "review", digest, body: '{"request_id":"original-request", "prepared_digest":"' + digest + '", "verdict":"approve", "rationale":"Exact rationale"}', ...changes });
const read = command => readIntegrationCommand(JSON.stringify(command), "library", "branch");
test("recovery preserves exact serialized bytes while routing only allowlisted operations", () => {
  assert.equal(read(record()).body, record().body);
  assert.equal(integrationEndpoint(read(record())), "/integrations/reviews");
  for (const changes of [{library:"foreign"}, {branch:"foreign"}, {action:"/admin/grants"}, {digest:"other"}, {body:'{"request_id":"invalid/path"}'}, {body:"null"}]) assert.throws(() => read(record(changes)));
  assert.throws(() => readIntegrationCommand(" ".repeat((2 << 20) + 1), "library", "branch"));
});
test("prepare recovery binds the displayed branch and exact rebind identity", () => {
  const body = { request_id:"original", branch_id:"branch", expected_head:"abc", expected_main:"def", expected_policy_revision:1, rebind_prepared_digest:digest };
  const command = record({ action:"prepare", body:JSON.stringify(body) });
  assert.deepEqual(read(command), command);
  for (const changes of [{branch_id:"foreign"}, {expected_head:null}, {expected_policy_revision:0}, {rebind_prepared_digest:"other"}]) assert.throws(() => read({...command, body:JSON.stringify({...body, ...changes})}));
});
test("evaluation and composition recovery reject malformed nested identities", () => {
  const budget = {max_tokens:1,max_cost_micros:0,max_attempts:1,timeout_seconds:60,max_artifact_bytes:1024};
  const evaluation = record({action:"evaluation",body:JSON.stringify({request_id:"original",requirement_digest:digest,budget,seed:0})});
  assert.equal(integrationEndpoint(read(evaluation)), `/integrations/${encodeURIComponent(digest)}/evaluations`);
  assert.throws(() => read({...evaluation,body:JSON.stringify({request_id:"original",requirement_digest:digest,budget:{...budget,max_tokens:-1},seed:0})}));
  const composition = record({action:"compose",digest:"",body:JSON.stringify({request_id:"original",expected_main:"abc",expected_policy_revision:1,branches:[null]})});
  assert.throws(() => read(composition));
});
