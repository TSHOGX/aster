import { Space } from "../../../components/space";
export default async function Page({ params }: { params: Promise<{ group: string }> }) {
  const { group } = await params;
  return <Space groupID={group} />;
}
