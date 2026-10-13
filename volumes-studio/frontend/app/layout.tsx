import type { Metadata } from "next";
import "./style.css";
import { StudioProvider } from "../components/studio-provider";
import { localeInitScript } from "../../../web/i18n/locale";
import { themeInitScript } from "../../../web/appearance";

export const metadata: Metadata = {
  title: "Volume Studio",
  description: "Author and publish immutable data volumes",
};
export const dynamic = "force-dynamic";
export default function Layout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeInitScript }} />
        <script dangerouslySetInnerHTML={{ __html: localeInitScript }} />
      </head>
      <body>
        <StudioProvider>{children}</StudioProvider>
      </body>
    </html>
  );
}
