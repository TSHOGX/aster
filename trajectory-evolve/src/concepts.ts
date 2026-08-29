// concepts.ts — deterministic concept tagging, shared by scoring and the mock.
//
// Track A scores an extractor version by comparing what it produced against a
// frozen human gold set. That comparison must NOT be done by an agent: letting
// the model grade its own output is how a self-evolving loop convinces itself it
// improved. So a statement is mapped to a concept key by rules here, and the
// score is arithmetic over concept keys.
//
// The rules are bilingual on purpose — real traces mix zh/en, and "current_curve
// is double-encoded JSON" and "充电曲线字段需要二次解析" are the same knowledge.

const CONCEPTS: Array<[RegExp, string]> = [
  [/工况码|工况枚举|work_?condition|working[ _-]?condition/i, "wc_enum"],
  [/分区|partition|覆盖|coverage|有没有数据|数据范围|时间范围|dt\b/i, "data_coverage"],
  [/权限|permission|compute[ _-]?group|denied|拒绝访问/i, "permission"],
  [/充电曲线|current_curve|双重编码|double[ -]?encoded|二次解析|嵌套 ?json/i, "curve_encoding"],
  [/字体|font|cjk|中文乱码|matplotlib/i, "cjk_font"],
  [/充电分析|charging[ _-]?analysis|charge_session|充电段/i, "charging_analysis"],
  [/\bsoc\b|荷电/i, "soc"],
  [/information_schema|表结构|schema 探测|describe/i, "schema_probe"],
];

/** The concept a statement is about, or a stable fallback key derived from its
 *  leading words (so unmatched statements still cluster with their paraphrases
 *  only when they really are textually the same). */
export function conceptOf(text: string): string {
  for (const [re, key] of CONCEPTS) if (re.test(text)) return key;
  const head = text
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s]/gu, " ")
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 3)
    .join("-");
  return `misc:${head || "empty"}`;
}

/** Canonical scope label. Free-text scopes ("vhr_data", "doris/bms", "数据源选择")
 *  are collapsed so equivalent statements land in the same clustering block. */
export function canonicalScope(scope: string, statement = ""): string {
  const t = `${scope} ${statement}`.toLowerCase();
  if (/工况|work_?condition/.test(t)) return "work_condition";
  if (/充电|charg/.test(t)) return "charging";
  if (/电池|battery|bms|soc|vhr/.test(t)) return "battery";
  if (/doris|sql|表|table|数据源|f2|schema/.test(t)) return "doris";
  if (/字体|font|matplotlib|plot|图/.test(t)) return "plotting";
  return "global";
}
