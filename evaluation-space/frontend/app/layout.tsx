import type { Metadata } from "next";
import { localeInitScript } from "../../../web/i18n/locale";
import { themeInitScript } from "../../../web/appearance";
import "./style.css";
import { SpaceShell } from "../components/space";
export const metadata: Metadata = { title: "Evaluation Space", description: "Group workflows, runs and evaluations" };
export const dynamic = "force-dynamic";
export default function Layout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="en" suppressHydrationWarning><head><script dangerouslySetInnerHTML={{ __html: themeInitScript }} /><script dangerouslySetInnerHTML={{ __html: localeInitScript }} /></head><body><SpaceShell>{children}</SpaceShell></body></html>;
}
