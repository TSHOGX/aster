# Facts: battery-domain-reference

Pointer note for the human-seeded battery engineering reference corpus. Durable
copy lives at `/agents/data/battery-domain/FACT.md` (this file under `facts/` is
regenerated on every KB republish; the `data/` copy and
`skills/battery-domain-reference/SKILL.md` are carried forward).

- **[reference]** BMS engineering **specifications** for the 智选车 platforms live at
  `/agents/data/battery-domain/`. Start at its `README.md` — the map of nine
  domains, join keys, search recipes and fidelity caveats.
  _(适用: any question needing a designed value rather than a measured one — fault
  code meaning, DTC mapping, CAN signal scale/offset/bit position, cell
  parameters, safety limits, thermal or insulation thresholds, charge-stop codes)_
- **[reference]** Coverage: 170 source files across `CAN信号矩阵` (CAN/CANFD
  matrices for F1, F1L F3, F2, X0, X1, X1 EV, X2, X4, X6, SHA, SHB, 奇文, 智驾版 +
  VHR 数据清单), `故障列表` (fault lists, UDS diagnostic questionnaires,
  `vhr_fault_config` exports), `电芯参数表` (37–120 kWh packs: 基本参数, P-I map,
  充电窗口, OCV, SOH, SOE, Safety Limit), `热失控诊断策略`, `热管理`, `绝缘检测`,
  `充电结束码`, `NTC布置`, `电气原理图`. Model years 2021–2026.
- **[reference]** Everything is searchable as text: 766 sheet CSVs under
  `tables/`, 41 CAN databases as `messages`/`signals` CSVs under `can/`
  (4,177 messages, 44,791 signals), PDF/PPTX text under `docs/`, and 4,590 Excel
  cell comments + 48,357 formulas under `meta/` (index: `META_INDEX.csv`).
  Locate via `SHEETS.csv` / `MANIFEST.csv` before opening anything large.
- **[guardrail]** This is the **text-only** build: no `raw/` originals, because
  this platform's model reads text, not images. 7 NTC / 热失控 layout PNGs and 12
  image-only PPTX slides — including the entire `CATL报警策略20240223.pptx` deck —
  are therefore NOT available, and physical cell/NTC positions cannot be answered.
  `IMAGE_ONLY_GAPS.csv` lists exactly what is missing; check it before telling a
  user something isn't in the corpus.
- **[reference]** Join keys across the corpus: **FaultNum** links fault lists to
  `vhr_fault_config` and to `BMS_FaultNum` CAN signals; **DTC code** links fault
  lists to UDS `DTC list` sheets; **CAN signal name** links `can/*.signals.csv`
  to the 热管理/绝缘检测/充电结束码 strategy tables; **pack capacity + 串并联**
  (e.g. `100kwh_1P168S`) identifies a cell parameter table and its matching
  `三过一欠 Fault List--<n>KWh` sheet.
  _(适用: questions spanning more than one domain)_
- **[guardrail]** Thresholds, fault levels and DTCs are **platform- and
  capacity-specific**; never generalize one across platforms. Multiple document
  revisions are kept deliberately (夏标/冬标/C样冻结, matrix V1.8.2 vs V1.8.5) —
  select by the platform and date in the filename and state which file and
  version the answer came from.
- **[guardrail]** The corpus carries 华为保密信息 / CATL Confidential / 【机密】
  markings. Use it to answer inside the platform; never transmit file contents to
  an external service or paste raw excerpts outside the sandbox.
- **[reference]** Known gap: `CAN信号矩阵/F2/F2EVR项目VIU1_EP网段Matrix V4.2.1
  2023.10.11_BMS.xlsx` is truncated in the source archive and unreadable (also in
  Excel); use the `V5.8.1 2024.8.28_BMS_EVR` matrix instead. 4 of 41 DBCs have
  overlapping signal definitions in the source and were parsed with relaxed
  checks — cross-check their bit positions against the sibling `.xlsx`.
  See `MANIFEST.csv` and `CONVERT_LOG.txt`.
- **[reference]** Pair with `skills/battery-domain-reference` for the lookup
  procedure, and with dingpan MCP (`dp-dingpan-mcp-server` via
  `skills/mcp-data-query`) to go from a decoded code or signal to its
  occurrences in fleet data. Do not open Doris/OBS skills for that step unless
  the user explicitly asked for those paths.
