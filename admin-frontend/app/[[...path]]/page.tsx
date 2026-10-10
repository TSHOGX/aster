import { Administration } from "@/components/administration";
export default async function Page({ params }: { params: Promise<{ path?: string[] }> }) {
  return <Administration route={(await params).path || []} />;
}
