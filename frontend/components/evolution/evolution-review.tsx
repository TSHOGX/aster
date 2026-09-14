"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Check,
  CheckCheck,
  ChevronDown,
  ChevronRight,
  FileCode2,
  GitCompare,
  Loader2,
  ShieldAlert,
  X
} from "lucide-react";
import styles from "./evolution-review.module.css";

type Decision = "approved" | "rejected";
type ReviewEntry = { fact_id: string; scope: string; body: string };
type ReviewLine = { kind: "add" | "del" | "ctx" | "hunk"; text: string };
type ReviewFile = {
  path: string;
  change: "added" | "removed" | "modified";
  per_entry: boolean;
  derived: boolean;
  entries?: ReviewEntry[];
  diff?: ReviewLine[];
  oversized?: boolean;
  bytes?: number;
  content?: string;
};
type Pending = { digest: string; prev_digest?: string; staged_at?: string; counts?: Record<string, number> };
type Review = { current: string; pending: Pending | null; files: ReviewFile[]; new_facts: number };

const changeLabel: Record<ReviewFile["change"], string> = { added: "新增", removed: "删除", modified: "修改" };
const fileKey = (file: ReviewFile) => file.path;

function decidableKeys(file: ReviewFile): string[] {
  if (file.derived) return [];
  if (file.per_entry) return (file.entries ?? []).map((entry) => entry.fact_id);
  return [fileKey(file)];
}

function folderOf(path: string) {
  const cut = path.lastIndexOf("/");
  return cut < 0 ? "/" : path.slice(0, cut);
}

function fileName(path: string) {
  return path.split("/").pop() || path;
}

function factSummary(body: string) {
  return body
    .replace(/^-\s*/, "")
    .replace(/\s+—\s+support=\d+,\s*confidence=[\d.]+\s*$/, "")
    .trim();
}

type PreviewLine = { text: string; active: boolean; factId: string };

function previewLines(content: string, activeFact: string): PreviewLine[] {
  const lines = content.split("\n");
  const out: PreviewLine[] = [];
  let currentId = "";
  for (const line of lines) {
    const marker = line.match(/^<!--\s*fact-id:(\d+)\s*-->$/);
    if (marker) {
      currentId = marker[1];
      out.push({ text: line, active: currentId === activeFact, factId: currentId });
      continue;
    }
    if (line.startsWith("#")) currentId = "";
    out.push({ text: line, active: currentId !== "" && currentId === activeFact, factId: currentId });
  }
  return out;
}

export function EvolutionReview() {
  const [review, setReview] = useState<Review | null>(null);
  const [selected, setSelected] = useState<string>("");
  const [selectedFact, setSelectedFact] = useState<string>("");
  const [decisions, setDecisions] = useState<Record<string, Decision>>({});
  const [checked, setChecked] = useState<Record<string, boolean>>({});
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [notice, setNotice] = useState("");
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const response = await fetch("/api/kb-review", { cache: "no-store" });
      const body = (await response.json()) as Review & { error?: string };
      if (!response.ok) throw new Error(body.error || `读取失败（${response.status}）`);
      setReview(body);
      setDecisions({});
      setChecked({});
      setFailed(false);
      setSelected((prev) => (body.files?.some((file) => file.path === prev) ? prev : body.files?.[0]?.path ?? ""));
      if (!body.pending) setNotice("当前没有待审批的快照。");
    } catch (error) {
      setFailed(true);
      setNotice(error instanceof Error ? error.message : "读取待审批快照失败。");
    } finally {
      setLoading(false);
    }
  }, []);

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(); }, [load]);

  const files = useMemo(() => review?.files ?? [], [review]);
  const activeFile = files.find((file) => file.path === selected) ?? null;

  const allKeys = useMemo(() => files.flatMap(decidableKeys), [files]);
  const undecided = allKeys.filter((key) => !decisions[key]);
  const approvedCount = allKeys.filter((key) => decisions[key] === "approved").length;
  const rejectedCount = allKeys.filter((key) => decisions[key] === "rejected").length;

  const groups = useMemo(() => {
    const byFolder = new Map<string, ReviewFile[]>();
    for (const file of files) {
      const folder = folderOf(file.path);
      byFolder.set(folder, [...(byFolder.get(folder) ?? []), file]);
    }
    return [...byFolder.entries()].sort(([left], [right]) => left.localeCompare(right));
  }, [files]);

  const decide = (keys: string[], decision: Decision) =>
    setDecisions((old) => ({ ...old, ...Object.fromEntries(keys.map((key) => [key, decision])) }));

  const fileStatus = (file: ReviewFile) => {
    const keys = decidableKeys(file);
    if (keys.length === 0) return { label: "派生", className: "" };
    const decided = keys.filter((key) => decisions[key]);
    if (decided.length === 0) return { label: `${keys.length} 待审`, className: "" };
    if (decided.length < keys.length) return { label: `${decided.length}/${keys.length}`, className: "" };
    const approved = keys.filter((key) => decisions[key] === "approved").length;
    if (approved === keys.length) return { label: "已批准", className: styles.approved };
    if (approved === 0) return { label: "已驳回", className: styles.rejected };
    return { label: `批准 ${approved}`, className: styles.approved };
  };

  const activeKeys = activeFile ? decidableKeys(activeFile) : [];
  const checkedKeys = activeKeys.filter((key) => checked[key]);
  const preview = useMemo(
    () => previewLines(activeFile?.content ?? "", selectedFact),
    [activeFile?.content, selectedFact]
  );

  useEffect(() => {
    if (!activeFile?.per_entry) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      if (selectedFact) setSelectedFact("");
      return;
    }
    const ids = (activeFile.entries ?? []).map((entry) => entry.fact_id);
    if (!ids.includes(selectedFact)) setSelectedFact(ids[0] ?? "");
  }, [activeFile, selectedFact]);

  useEffect(() => {
    if (!selectedFact) return;
    document.getElementById(`fact-${selectedFact}`)?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [selectedFact, activeFile?.path]);

  const openFile = (path: string) => setSelected(path);

  const submit = async (publish: boolean) => {
    if (!review?.pending) return;
    setBusy(true);
    try {
      const facts: Record<string, Decision> = {};
      const fileDecisions: Record<string, Decision> = {};
      for (const file of files) {
        if (file.derived) continue;
        if (file.per_entry) {
          for (const entry of file.entries ?? []) {
            const decision = decisions[entry.fact_id];
            if (decision) facts[entry.fact_id] = decision;
          }
        } else {
          const decision = decisions[fileKey(file)];
          if (decision) fileDecisions[file.path] = decision;
        }
      }
      const response = await fetch("/api/kb-review/decisions", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ digest: review.pending.digest, facts, files: fileDecisions, publish })
      });
      const body = await response.json();
      if (!response.ok) throw new Error(body.error || `提交失败（${response.status}）`);
      const parts = [
        body.published
          ? `已发布快照 ${body.digest}，新会话将挂载它。`
          : body.staged
            ? `已重新聚合为待审快照 ${body.digest}。`
            : "本次没有批准任何新内容，知识库保持不变。",
        `批准 ${body.approved_facts} 条 fact、${body.approved_files} 个文件；驳回 ${body.rejected_facts} 条、${body.rejected_files} 个文件。`
      ];
      if (body.rejected_facts > 0) {
        parts.push(
          body.rejected_durable
            ? "被驳回的 fact 已在记忆库中降级，今晚不会重新生成。"
            : "注意：被驳回的 fact 未能全部降级，可能在下一轮重新出现，请检查 orchestrator 日志。"
        );
      }
      setNotice(parts.join(" "));
      setFailed(false);
      await load();
    } catch (error) {
      setFailed(true);
      setNotice(error instanceof Error ? error.message : "提交失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className={styles.page}>
      <div className={styles.shell}>
        <header className={styles.header}>
          <div>
            <a className={styles.back} href="/evolution">
              返回轨迹自进化结果
            </a>
            <div className={styles.eyebrow}>
              <GitCompare size={15} /> KNOWLEDGE BASE · APPROVAL
            </div>
            <h1>知识库变更审批</h1>
            <p>左侧选择文件，中间批准或驳回 fact，右侧查看该文件的完整内容。</p>
          </div>
          <div className={styles.headerMeta}>
            <span className={styles.pending}>
              <i />
              {review?.pending ? "待审批" : "无待审内容"}
            </span>
            <code>pending · {review?.pending?.digest ?? (loading ? "加载中" : "—")}</code>
          </div>
        </header>

        <section className={styles.summary}>
          <div>
            <span>暂存快照</span>
            <strong>{review?.pending?.digest ?? "—"}</strong>
            <small>基于 {review?.current || "（尚未发布任何快照）"}</small>
          </div>
          <div>
            <span>可决策条目</span>
            <strong>{allKeys.length}</strong>
            <small>
              {review?.new_facts ?? 0} 条新增 fact · {undecided.length} 条待决策
            </small>
          </div>
          <div>
            <span>当前决策</span>
            <strong>
              <em>批准 {approvedCount}</em> / <b>驳回 {rejectedCount}</b>
            </strong>
            <small>未决策的条目不会被发布</small>
          </div>
          <button className={styles.approveAll} onClick={() => decide(allKeys, "approved")} disabled={!allKeys.length}>
            <CheckCheck size={17} />
            全部批准
          </button>
        </section>

        {notice && <div className={`${styles.notice} ${failed ? styles.noticeError : ""}`}>{notice}</div>}

        <section className={styles.workspace}>
          <aside className={styles.filePane}>
            <div className={styles.paneTitle}>
              <div>
                <strong>变更文件</strong>
                <small>{files.length} 个文件</small>
              </div>
              <span>{undecided.length} 待审</span>
            </div>
            <div className={styles.fileList}>
              {loading && <div className={styles.emptyPane}>加载中…</div>}
              {!loading && files.length === 0 && <div className={styles.emptyPane}>没有需要审批的变更。</div>}
              {groups.map(([folder, entries]) => (
                <div className={styles.fileGroup} key={folder}>
                  <button
                    className={styles.folderRow}
                    onClick={() => setCollapsed((old) => ({ ...old, [folder]: !old[folder] }))}
                  >
                    {collapsed[folder] ? <ChevronRight size={14} /> : <ChevronDown size={14} />}
                    <strong>{folder}</strong>
                    <small>{entries.length}</small>
                  </button>
                  {!collapsed[folder] &&
                    entries.map((file) => {
                      const status = fileStatus(file);
                      return (
                        <button
                          key={file.path}
                          className={`${styles.fileRow} ${file.path === selected ? styles.fileSelected : ""}`}
                          onClick={() => openFile(file.path)}
                        >
                          <FileCode2 size={16} />
                          <div>
                            <strong>{fileName(file.path)}</strong>
                            <small>
                              {changeLabel[file.change]}
                              {file.per_entry ? ` · ${(file.entries ?? []).length} 条` : ""}
                            </small>
                          </div>
                          <span className={`${styles.fileStatus} ${status.className}`}>{status.label}</span>
                        </button>
                      );
                    })}
                </div>
              ))}
            </div>
          </aside>

          <section className={styles.factsPane}>
            {!activeFile && <div className={styles.emptyPane}>{loading ? "加载中…" : "选择左侧的文件查看待审 fact。"}</div>}
            {activeFile && (
              <>
                <div className={styles.diffHeader}>
                  <div>
                    <div className={styles.path}>
                      <strong>{fileName(activeFile.path)}</strong>
                      <span>{changeLabel[activeFile.change]}</span>
                    </div>
                    <p>
                      {activeFile.derived
                        ? "这是索引文件，由审批结果自动重算，不需要单独决策。"
                        : activeFile.per_entry
                          ? "在这里批准或驳回本文件的新增 fact；右侧同步显示完整文件。"
                          : "该文件按整档决策：批准即采用暂存版本，驳回则保留当前线上版本。"}
                    </p>
                  </div>
                  {activeKeys.length > 0 && (
                    <div className={styles.actions}>
                      <button className={styles.reject} onClick={() => decide(activeKeys, "rejected")}>
                        <X size={15} />
                        本文件全驳回
                      </button>
                      <button className={styles.approve} onClick={() => decide(activeKeys, "approved")}>
                        <Check size={15} />
                        本文件全批准
                      </button>
                    </div>
                  )}
                </div>

                {activeFile.per_entry && (
                  <div className={styles.diffToolbar}>
                    <label className={styles.selectAll}>
                      <input
                        type="checkbox"
                        checked={checkedKeys.length === activeKeys.length && activeKeys.length > 0}
                        onChange={(event) =>
                          setChecked((old) => ({
                            ...old,
                            ...Object.fromEntries(activeKeys.map((key) => [key, event.target.checked]))
                          }))
                        }
                      />
                      全选（已选 {checkedKeys.length}）
                    </label>
                    <button
                      className={styles.bulkReject}
                      disabled={!checkedKeys.length}
                      onClick={() => decide(checkedKeys, "rejected")}
                    >
                      <X size={13} />
                      驳回所选
                    </button>
                    <button
                      className={styles.bulkApprove}
                      disabled={!checkedKeys.length}
                      onClick={() => decide(checkedKeys, "approved")}
                    >
                      <Check size={13} />
                      批准所选
                    </button>
                  </div>
                )}

                <div className={styles.diff}>
                  {activeFile.per_entry &&
                    (activeFile.entries ?? []).map((entry) => {
                      const decision = decisions[entry.fact_id];
                      return (
                        <article
                          key={entry.fact_id}
                          className={`${styles.entry} ${
                            decision === "approved"
                              ? styles.entryApproved
                              : decision === "rejected"
                                ? styles.entryRejected
                                : ""
                          } ${selectedFact === entry.fact_id ? styles.entryActive : ""}`}
                          onClick={() => setSelectedFact(entry.fact_id)}
                        >
                          <div className={styles.entryHead}>
                            <label className={styles.entryPick}>
                              <input
                                type="checkbox"
                                checked={Boolean(checked[entry.fact_id])}
                                onChange={(event) =>
                                  setChecked((old) => ({ ...old, [entry.fact_id]: event.target.checked }))
                                }
                              />
                              <span>
                                <code>#{entry.fact_id}</code>
                                {entry.scope}
                              </span>
                            </label>
                            <div className={styles.entryActions}>
                              {decision && (
                                <span className={decision === "approved" ? styles.tagApproved : styles.tagRejected}>
                                  {decision === "approved" ? "已批准" : "已驳回"}
                                </span>
                              )}
                              <button
                                className={styles.approve}
                                onClick={() => decide([entry.fact_id], "approved")}
                                disabled={decision === "approved"}
                              >
                                <Check size={14} />
                                批准
                              </button>
                              <button
                                className={styles.reject}
                                onClick={() => decide([entry.fact_id], "rejected")}
                                disabled={decision === "rejected"}
                              >
                                <X size={14} />
                                驳回
                              </button>
                            </div>
                          </div>
                          <p className={styles.entryBody}>{factSummary(entry.body)}</p>
                        </article>
                      );
                    })}

                  {activeFile.per_entry && (activeFile.entries ?? []).length === 0 && (
                    <div className={styles.emptyPane}>该文件没有新增 fact（内容变化来自已发布条目的重排）。</div>
                  )}

                  {!activeFile.per_entry && activeFile.oversized && (
                    <div className={styles.emptyPane}>
                      文件较大（{Math.round((activeFile.bytes ?? 0) / 1024)} KB），已省略逐行 diff。请用
                      <code> orchestrator kb diff </code>查看细节后再决策。
                    </div>
                  )}

                  {!activeFile.per_entry &&
                    !activeFile.oversized &&
                    (activeFile.diff ?? []).map((line, index) => (
                      <div
                        key={index}
                        className={`${styles.line} ${
                          line.kind === "add"
                            ? styles.add
                            : line.kind === "del"
                              ? styles.del
                              : line.kind === "hunk"
                                ? styles.hunk
                                : ""
                        }`}
                      >
                        <span>{line.kind === "add" ? "+" : line.kind === "del" ? "-" : ""}</span>
                        <code>{line.text || " "}</code>
                      </div>
                    ))}

                  {!activeFile.per_entry && !activeFile.oversized && (activeFile.diff ?? []).length === 0 && (
                    <div className={styles.emptyPane}>该文件没有可展示的 diff，请看右侧完整内容。</div>
                  )}
                </div>
              </>
            )}

            <div className={styles.diffFooter}>
              <div className={styles.safety}>
                <ShieldAlert size={16} />
                <span>
                  {undecided.length > 0
                    ? `还有 ${undecided.length} 条未决策；未决策的条目不会被发布。`
                    : "所有条目已决策，可以提交。"}
                </span>
              </div>
              <div className={styles.actions}>
                <button className={styles.reject} onClick={() => decide(allKeys, "rejected")} disabled={!allKeys.length}>
                  <X size={16} />
                  全部驳回
                </button>
                <button className={styles.approve} onClick={() => void submit(false)} disabled={busy || !review?.pending}>
                  {busy ? <Loader2 className={styles.spin} size={16} /> : <Check size={16} />}
                  {busy ? "提交中…" : "提交并重新暂存"}
                </button>
              </div>
            </div>
          </section>

          <section className={styles.previewPane}>
            <div className={styles.paneTitle}>
              <div>
                <strong>完整文件</strong>
                <small>{activeFile?.path || "未选择文件"}</small>
              </div>
            </div>
            <div className={styles.preview}>
              {!activeFile && <div className={styles.emptyPane}>{loading ? "加载中…" : "选择文件后在这里查看全文。"}</div>}
              {activeFile?.oversized && !activeFile.content && (
                <div className={styles.emptyPane}>
                  文件较大（{Math.round((activeFile.bytes ?? 0) / 1024)} KB），已省略全文预览。
                </div>
              )}
              {activeFile &&
                !activeFile.content &&
                !activeFile.oversized && <div className={styles.emptyPane}>该文件没有可展示的正文。</div>}
              {activeFile?.content &&
                preview.map((line, index) => (
                  <div
                    key={index}
                    id={line.factId && preview[index - 1]?.factId !== line.factId ? `fact-${line.factId}` : undefined}
                    className={`${styles.previewLine} ${line.active ? styles.previewActive : ""} ${line.factId ? styles.previewClickable : ""}`}
                    onClick={() => {
                      if (line.factId) setSelectedFact(line.factId);
                    }}
                  >
                    <code>{line.text || " "}</code>
                  </div>
                ))}
            </div>
          </section>
        </section>

        <footer className={styles.footer}>
          <span>
            “提交并重新暂存”只会把批准的内容重新聚合为新的待审快照，不会上线；“批准并发布”会立即把它设为
            current，新建会话开始挂载。
          </span>
          <button onClick={() => void submit(true)} disabled={busy || !review?.pending}>
            批准并发布
          </button>
        </footer>
      </div>
    </main>
  );
}
