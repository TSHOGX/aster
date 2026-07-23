import { describe, expect, it } from "vitest";

import { parseApiError } from "./api";

describe("parseApiError", () => {
  it("extracts error and error_class from a JSON body", () => {
    const body = JSON.stringify({ error: "stale edit", error_class: "stale_edit" });
    expect(parseApiError(body, "Conflict", "save failed")).toEqual({
      error: "stale edit",
      errorClass: "stale_edit"
    });
  });

  it("returns error without errorClass when error_class is absent", () => {
    const body = JSON.stringify({ error: "nope" });
    expect(parseApiError(body, "Bad Request", "upload failed")).toEqual({
      error: "nope",
      errorClass: undefined
    });
  });

  it("falls back to statusText for a non-JSON body", () => {
    expect(parseApiError("<html>500</html>", "Server Error", "request failed")).toEqual({
      error: "Server Error",
      errorClass: undefined
    });
  });

  it("falls back to the fallback message when statusText is empty", () => {
    expect(parseApiError("", "", "request failed")).toEqual({
      error: "request failed",
      errorClass: undefined
    });
  });

  it("ignores a non-string error field", () => {
    const body = JSON.stringify({ error: 42 });
    expect(parseApiError(body, "Teapot", "boom")).toEqual({
      error: "Teapot",
      errorClass: undefined
    });
  });
});
