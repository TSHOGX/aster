import test from "node:test";
import assert from "node:assert/strict";
import { currentReviews } from "./reviews.ts";
const review = (id, issuer, subject, verdict) => ({ id, reviewer: { issuer, subject }, verdict, rationale: "" });
test("withdrawn approvals do not remain current", () => {
 const history = [review("one","issuer","expert","approve"),review("two","issuer","expert","changes_requested")];
 assert.deepEqual(currentReviews(history).map(r=>r.id),["two"]);
 assert.equal(history.length,2);
});
test("reviewer identity includes issuer and later approval supersedes rejection", () => {
 const history = [review("one","a","expert","reject"),review("two","b","expert","approve"),review("three","a","expert","approve")];
 assert.deepEqual(currentReviews(history).map(r=>r.id).sort(),["three","two"]);
});
