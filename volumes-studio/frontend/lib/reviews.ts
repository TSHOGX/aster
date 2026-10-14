export type ReviewDecision = {
  id: string;
  reviewer: { issuer: string; subject: string };
  verdict: string;
  rationale: string;
};

// The API orders decisions by durable insertion order. Timestamps alone cannot
// distinguish multiple changes made within the same second.
export function currentReviews(history: ReviewDecision[]): ReviewDecision[] {
  const latest = new Map<string, ReviewDecision>();
  for (const review of history) {
    latest.set(JSON.stringify([review.reviewer.issuer, review.reviewer.subject]), review);
  }
  return [...latest.values()];
}
