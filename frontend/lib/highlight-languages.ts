import { common } from "lowlight";
import matlab from "highlight.js/lib/languages/matlab";
import julia from "highlight.js/lib/languages/julia";
import fortran from "highlight.js/lib/languages/fortran";
import ini from "highlight.js/lib/languages/ini";
import dockerfile from "highlight.js/lib/languages/dockerfile";

/**
 * The grammar registry handed to rehype-highlight.
 *
 * The `...common` spread is load-bearing and easy to delete by accident: the
 * plugin's `languages` option *replaces* its default registry rather than
 * extending it. Passing only the extras therefore unregisters Python, bash, JSON
 * and the other 30-odd common grammars — which is most of what the agent writes,
 * so every code block in the conversation silently renders as flat monochrome
 * text while the artifact viewer (which calls highlight.js directly, with the
 * full library) keeps working. That asymmetry makes the cause hard to spot from
 * the symptom, hence the test alongside this file.
 *
 * Beyond `common`: MATLAB is the lingua franca of battery/BMS work, Julia and
 * Fortran show up in simulation code, and TOML/Dockerfile cover configs the
 * agent edits. TOML uses highlight.js's `ini` grammar, which is what upstream
 * registers it under.
 */
export const HIGHLIGHT_LANGUAGES = {
  ...common,
  matlab,
  julia,
  fortran,
  toml: ini,
  dockerfile
};

/** Grammars this deployment adds on top of lowlight's `common` set. */
export const EXTRA_LANGUAGE_NAMES = ["matlab", "julia", "fortran", "toml", "dockerfile"] as const;

/**
 * Grammars the agent uses constantly. Asserted by the test suite: if a refactor
 * drops the `common` spread these disappear, and nothing else fails.
 */
export const ESSENTIAL_LANGUAGE_NAMES = [
  "python",
  "bash",
  "shell",
  "json",
  "javascript",
  "typescript",
  "yaml",
  "sql",
  "diff",
  "markdown"
] as const;
