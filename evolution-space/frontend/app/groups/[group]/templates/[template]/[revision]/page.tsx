import { TemplateDetailPage } from "../../../../../../components/sections";
export default async function Page({ params }: { params: Promise<{ template: string; revision: string }> }) {
  const { template, revision } = await params;
  return <TemplateDetailPage id={template} revision={Number(revision)} />;
}
