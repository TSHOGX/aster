import { PublicShare } from "@/components/project/public-share";
export default async function SharedPage({
  params,
}: {
  params: Promise<{ token: string }>;
}) {
  const { token } = await params;
  return <PublicShare token={token} />;
}
