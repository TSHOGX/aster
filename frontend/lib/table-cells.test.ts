import { describe, expect, it } from "vitest";

import { isNumericText } from "./table-cells";

describe("isNumericText", () => {
  it("accepts plain integers and decimals", () => {
    expect(isNumericText("42")).toBe(true);
    expect(isNumericText("0")).toBe(true);
    expect(isNumericText("3.1415")).toBe(true);
    expect(isNumericText("0.0")).toBe(true);
  });

  it("accepts signed values", () => {
    expect(isNumericText("-17")).toBe(true);
    expect(isNumericText("+2.5")).toBe(true);
    expect(isNumericText("-0.004")).toBe(true);
  });

  it("accepts grouped thousands", () => {
    expect(isNumericText("1,000")).toBe(true);
    expect(isNumericText("12,345,678")).toBe(true);
    expect(isNumericText("-2,500.75")).toBe(true);
  });

  it("rejects malformed grouping", () => {
    expect(isNumericText("1,00")).toBe(false);
    expect(isNumericText("12,3456")).toBe(false);
    expect(isNumericText(",100")).toBe(false);
  });

  it("accepts exponent notation", () => {
    expect(isNumericText("1e6")).toBe(true);
    expect(isNumericText("2.5E-3")).toBe(true);
    expect(isNumericText("-1.1e+12")).toBe(true);
  });

  it("accepts a trailing percent", () => {
    // Percent is a scale factor rather than a unit, and capacity-retention and
    // efficiency columns are overwhelmingly written this way.
    expect(isNumericText("80%")).toBe(true);
    expect(isNumericText("99.94%")).toBe(true);
    expect(isNumericText("-1.2%")).toBe(true);
  });

  it("rejects text, blanks and mixed content", () => {
    expect(isNumericText("")).toBe(false);
    expect(isNumericText("   ")).toBe(false);
    expect(isNumericText("N/A")).toBe(false);
    expect(isNumericText("Cell A")).toBe(false);
    // A figure with a unit is prose: right-aligning it would break the column.
    expect(isNumericText("3 cycles")).toBe(false);
    expect(isNumericText("4.2 V")).toBe(false);
    expect(isNumericText("120mAh")).toBe(false);
  });

  it("rejects ranges and multi-number cells", () => {
    expect(isNumericText("10-20")).toBe(false);
    expect(isNumericText("1.2 / 3.4")).toBe(false);
  });

  it("ignores surrounding whitespace", () => {
    expect(isNumericText("  55.5  ")).toBe(true);
    expect(isNumericText("\t7\n")).toBe(true);
  });
});
