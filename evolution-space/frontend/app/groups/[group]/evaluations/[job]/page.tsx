import { EvaluationReport } from "../../../../../components/evaluation-report";

export default async function Page({ params }: { params: Promise<{ group: string; job: string }> }) {
  const value = await params;
  return <EvaluationReport group={value.group} job={value.job} />;
}
