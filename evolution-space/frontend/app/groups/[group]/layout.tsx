import { GroupLayout } from "../../../components/group-layout";
export default async function Layout({ params, children }: { params: Promise<{ group: string }>; children: React.ReactNode }) {
  const { group } = await params;
  return <GroupLayout id={group}>{children}</GroupLayout>;
}
