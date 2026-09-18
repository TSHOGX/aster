// Deterministic, synthetic workload shared by browser correctness and perf runs.
export function messages(count: number, sessionId = "synthetic") {
  return Array.from({ length: count }, (_, i) => ({
    id: i + 1,
    session_id: sessionId,
    role: i % 3 === 0 ? "user" : "assistant",
    content:
      i % 3 === 0
        ? `Analyze sample ${i}`
        : `Sample ${i}\n\n| Cycle | Capacity |\n| --- | --- |\n| 1 | 98.4 |\n\n\`\`\`python\nprint(${i})\n\`\`\`\n`,
    created_at: "2026-09-18T00:00:00Z",
    stop_reason: i % 3 === 0 ? "" : "stop",
    is_final_reply: i % 3 !== 0,
    has_trace: false,
    content_truncated: false,
  }));
}
export function artifacts(count: number, sessionId = "synthetic") {
  return Array.from({ length: count }, (_, i) => ({
    session_id: sessionId,
    path: `group${i % 100}/sub${i % 10}/file${String(i).padStart(7, "0")}.txt`,
    size: 1000,
    mod_time: "2026-09-18T00:00:00Z",
    created_at: "2026-09-18T00:00:00Z",
    updated_at: "2026-09-18T00:00:00Z",
  }));
}
