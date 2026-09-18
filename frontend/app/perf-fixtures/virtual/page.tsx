import { notFound } from "next/navigation";
import { VirtualFixture } from "./virtual-fixture";

export const dynamic = "force-dynamic";
export default function Page() {
  if (process.env.HARNESS_PERF_FIXTURES !== "1") notFound();
  return <VirtualFixture />;
}
