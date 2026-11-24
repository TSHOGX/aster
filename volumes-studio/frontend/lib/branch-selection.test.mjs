import test from "node:test";
import assert from "node:assert/strict";
import { applyBranchSelection } from "./branch-selection.ts";

const digest = `sha256:${"ab".repeat(32)}`;

function search(previous, next, query) {
  const url = new URL(`https://studio.local/?${query}`);
  applyBranchSelection(url, previous, next);
  return url.searchParams;
}

test("switching branches drops the previous branch's review evidence", () => {
  const params = search("branch-a", "branch-b", `branch=branch-a&integration=${digest}&conflict=workspace&publication=version-1&history_head=${digest}&version=version-1&view=review`);
  assert.equal(params.get("branch"), "branch-b");
  assert.equal(params.get("view"), "review");
  for (const key of ["integration", "conflict", "publication", "history_head", "version"]) assert.equal(params.get(key), null);
});

test("selecting the current branch keeps its review evidence", () => {
  const params = search("branch-a", "branch-a", `branch=branch-a&integration=${digest}&conflict=workspace&publication=version-1&history_head=${digest}&version=version-1`);
  assert.equal(params.get("branch"), "branch-a");
  assert.equal(params.get("integration"), digest);
  assert.equal(params.get("conflict"), "workspace");
  assert.equal(params.get("publication"), "version-1");
  assert.equal(params.get("history_head"), digest);
  assert.equal(params.get("version"), null);
});

test("the first branch selection does not inherit leftover review evidence", () => {
  const params = search(undefined, "branch-a", `integration=${digest}&conflict=workspace&history_head=${digest}`);
  assert.equal(params.get("branch"), "branch-a");
  for (const key of ["integration", "conflict", "history_head"]) assert.equal(params.get(key), null);
});
