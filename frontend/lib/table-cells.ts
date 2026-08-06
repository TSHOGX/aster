/**
 * Numeric-cell detection for markdown tables.
 *
 * The agent's replies lean heavily on tables of measurements, and a numeric
 * column only reads correctly when its digits are right-aligned and set in
 * tabular figures. CSS cannot inspect text content, so cells are classified here
 * and tagged with a data attribute for globals.css to style.
 *
 * Lives in lib/ rather than beside the renderer so it can be unit-tested: the
 * predicate is where the judgement calls sit (is "3 cycles" numeric? is "N/A"?),
 * and those are easy to regress.
 */

/**
 * Optionally signed, with either grouped thousands or plain digits, an optional
 * decimal part, an optional exponent, and an optional trailing percent.
 */
const NUMERIC = /^[+-]?(\d{1,3}(,\d{3})+|\d+)(\.\d+)?([eE][+-]?\d+)?%?$/;

/**
 * True when the text is nothing but a number.
 *
 * Deliberately strict. A cell mixing digits with words ("3 cycles", "12 V") is
 * prose that happens to contain a figure: right-aligning it would break the
 * column's left edge for no gain. Values carrying a unit are therefore treated as
 * text, with percent the single exception — it is a pure scale factor, not a unit,
 * and percent columns are common enough in capacity/efficiency tables to matter.
 */
export function isNumericText(text: string): boolean {
  const trimmed = text.trim();
  if (trimmed === "") return false;
  return NUMERIC.test(trimmed);
}
