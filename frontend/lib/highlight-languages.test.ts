import { describe, expect, it } from "vitest";
import { common } from "lowlight";

import {
  ESSENTIAL_LANGUAGE_NAMES,
  EXTRA_LANGUAGE_NAMES,
  HIGHLIGHT_LANGUAGES
} from "./highlight-languages";

describe("HIGHLIGHT_LANGUAGES", () => {
  // The regression this file exists for: rehype-highlight's `languages` option
  // replaces its registry instead of extending it, so dropping the `...common`
  // spread unregisters everything the agent commonly writes. The conversation
  // then renders flat monochrome code while the artifact viewer stays correct,
  // which is a confusing symptom to trace back to this line.
  it("keeps the languages the agent writes most", () => {
    for (const name of ESSENTIAL_LANGUAGE_NAMES) {
      expect(HIGHLIGHT_LANGUAGES, `${name} must stay registered`).toHaveProperty(name);
    }
  });

  it("retains every grammar from lowlight's common set", () => {
    for (const name of Object.keys(common)) {
      expect(HIGHLIGHT_LANGUAGES).toHaveProperty(name);
    }
  });

  it("adds the deployment-specific grammars", () => {
    for (const name of EXTRA_LANGUAGE_NAMES) {
      expect(HIGHLIGHT_LANGUAGES, `${name} must be registered`).toHaveProperty(name);
    }
  });

  it("registers every entry as a grammar function", () => {
    // A non-function value makes lowlight throw on register, which takes out
    // highlighting wholesale rather than for the one language.
    for (const [name, grammar] of Object.entries(HIGHLIGHT_LANGUAGES)) {
      expect(typeof grammar, `${name} must be a function`).toBe("function");
    }
  });

  it("is strictly larger than the common set", () => {
    expect(Object.keys(HIGHLIGHT_LANGUAGES).length).toBeGreaterThan(Object.keys(common).length);
  });
});
