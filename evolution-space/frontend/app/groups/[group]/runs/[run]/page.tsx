import { RunInspector } from "../../../../../components/run-inspector";

export default async function Page({ params }: { params: Promise<{ group: string; run: string }> }) {
  const value = await params;
  return <RunInspector group={value.group} run={value.run} />;
}
