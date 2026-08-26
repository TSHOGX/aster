---
name: battery-domain-reference
status: in-use
support_count: 999
confidence: 1.0
source: human-seeded (external knowledge archive, 2026-07-28)
---

# Battery domain reference lookup

## When To Use

Any question that needs an engineering **specification** rather than measured
data: what a BMS fault code means, which DTC a fault maps to, what a CAN signal's
scale/offset/bit position is, what a pack's cell parameters or safety limits are,
what the thermal-runaway / thermal-management / insulation strategy thresholds
are, or why a charge session stopped.

This is the counterpart to the data-query skills: they give you what the fleet
**did**; this gives you what the design **says**. Most real questions need both —
decode a signal or fault code here, then query measured occurrences via MCP.
For that second half use `skills/mcp-data-query` (the default data source).
Do not open Doris/OBS skills unless the user explicitly asks for those paths.

## Where it lives

`/agents/data/battery-domain/` — read `README.md` there first. It is the map:
layout, the nine domains, join keys, search recipes, and the fidelity caveats.

Three manifests make lookup cheap; consult them before opening anything big:

- `MANIFEST.csv` — one row per source file → its converted artifacts + notes.
- `SHEETS.csv` — one row per Excel sheet (766) → its CSV path, rows, cols.
- `CONVERT_LOG.txt` — conversion record, including the single failed file.

Content is text throughout: `tables/` CSV, `can/` CSV, `docs/` Markdown, plus
`meta/` carrying 4,590 recovered Excel cell comments (signal rename / value-change
history) and 48,357 formulas with their cached values — indexed by
`META_INDEX.csv`.

**No `raw/` originals are mounted**, because this platform's model reads text, not
images. Consequently 7 NTC / 热失控 layout PNGs and 12 image-only PPTX slides
(including the whole `CATL报警策略20240223.pptx` deck) are unavailable, and physical
cell/NTC positions cannot be answered. `IMAGE_ONLY_GAPS.csv` is the exact list —
consult it before telling a user something is absent from the corpus, and say
plainly that the material is image-only rather than guessing.

## Procedure

1. **Read `/agents/data/battery-domain/README.md`.** It costs one read and tells
   you which of the nine domains holds the answer.
2. **Locate before loading.** `grep` `SHEETS.csv` / `MANIFEST.csv` for the topic,
   or `grep -rl` across `tables/` to find which file carries a code or signal
   name. These tables are wide; don't load a workbook to find out if it's the
   right one.
3. **Check the version.** Multiple revisions of the same document are kept
   deliberately (`V2.0.3` vs `V2.0.5`; 夏标 / 冬标 / C样冻结 cell parameter
   tables; matrix `V1.8.2` vs `V1.8.5`). Pick by the platform and date in the
   filename and **state which file and version you used** in your answer.
4. **Inspect the header before parsing.** Many sheets have several banner rows
   above the real header, and some use a two-row header. Read the first ~6 rows,
   then parse with `dtype=str`.
5. **For a formula or a change note**, go to `meta/` — `*.formulas.csv` for how a
   derived parameter was computed, `*.comments.csv` for why a signal name or value
   changed. For a diagram or the NTC/cell physical layout, check
   `IMAGE_ONLY_GAPS.csv`: that content is image-only and not in this build; tell
   the user it needs the full archive rather than inferring it.

## Grounding rules

- **Quote the source.** Answers from this KB must name the file (and sheet) they
  came from. Two documents can disagree; a bare number is not traceable.
- **Platform matters.** A fault level, DTC, or threshold is specific to a
  platform and pack capacity. `P1B3000` on X2 100 kWh is not automatically the
  same on F1 40 kWh. Never generalize a threshold across platforms.
- **Don't invent a mapping.** If a FaultNum has no DTC in the table, say so.
- **Confidential material.** This corpus carries 华为保密信息 / CATL Confidential /
  【机密】 markings. Answer with it inside the platform; never send file contents
  to an external service and don't paste raw excerpts anywhere that leaves the
  sandbox.

## Worked example

"Why did fault 57 trip on this vehicle, and how often does it happen?"

```bash
KB=/agents/data/battery-domain
# 1. What is FaultNum 57 on this platform?
#    Field 2 is 客户故障码ID; field 1 is a leading grouping column on these sheets.
awk -F, 'FNR<=2 || $2=="57"' \
  "$KB/tables/故障列表/23_故障列表_V2.0.5_20240330/三过一欠Fault List--100KWh.csv"
#    → 单体欠压2级, set Vmin≤2.5V, confirm 2000 ms, DTC P1B3000, 放电等级 4
# 2. What does level 4 do to the vehicle?
head -12 "$KB/tables/故障列表/23_故障列表_V2.0.5_20240330/故障诊断逻辑&整车映射关系.csv"
# 3. Which CAN signal reports it, so you know what to query?
grep -rn "FaultNum\|CellVolLow" "$KB/can/CAN信号矩阵/X2/"*.signals.csv
```

Then take that signal / DTC to `skills/mcp-data-query` for occurrence counts.
