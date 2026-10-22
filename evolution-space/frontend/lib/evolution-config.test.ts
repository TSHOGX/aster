import { expect, it } from "vitest";
import { bindingFromTemplate, configurationIntegersAreSafe, draftConfiguration, freshConfiguration, selectSource, type TemplateDefinition } from "./evolution-config";
const ref = { id: "skills", revision: 1, digest: `sha256:${"a".repeat(64)}` };
const template: TemplateDefinition = { ref, goal: "Build skills", profile: "shared-assets-v1", workflows: [ref], suggested_prefix: "skills", inputs: { corpus: "schema" }, outputs: { candidate: "schema" }, parameters_schema: { type: "object", properties: { count: { default: 3 } } }, evaluation: { mode: "disabled", reference_strategy: "none" } };
it("new bindings select immutable template/workflow refs without sharing mutable policy", () => {
  const config = freshConfiguration("group");
  const first = bindingFromTemplate(template, ref, config.budget, []);
  const second = bindingFromTemplate(template, ref, config.budget, [first]);
  expect(second.id).not.toBe(first.id); expect(first.parameters).toEqual({ count: 3 });
  first.budget.max_tokens = 1; expect(config.budget.max_tokens).not.toBe(1);
  expect(draftConfiguration({ ...config, revision: 12, auto_enabled: true })).toMatchObject({ revision: 0, auto_enabled: false });
});
it("removing source selections also removes their input references", () => {
  const config = freshConfiguration("group"), source = { id: "source", kind: "project" as const, resource_id: "project", policy_revision: 1, audience_groups: ["group"], derivative_publication: true, exclusions: [] };
  config.sources = [source]; const binding = bindingFromTemplate(template, ref, config.budget, []);
  binding.source_ids = [source.id]; binding.inputs = { corpus: { sources: [source.id] } }; config.bindings = [binding];
  expect(selectSource(config, source, false)).toMatchObject({ sources: [], bindings: [{ source_ids: [], inputs: { corpus: { sources: [] } } }] });
  expect(config.bindings[0].source_ids).toEqual([source.id]);
});

it("does not allow saving rounded contract integers", () => {
 const config=freshConfiguration("group");expect(configurationIntegersAreSafe(config)).toBe(true);
 config.budget.max_cost_micros=Number.MAX_SAFE_INTEGER+1;expect(configurationIntegersAreSafe(config)).toBe(false);
});
