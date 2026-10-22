import type { Metadata } from "next";
import localFont from "next/font/local";
import type { ReactNode } from "react";
import { AppProviders } from "@/_app/providers";
import "@/_app/styles/globals.css";

const alfa = localFont({
  src: [
    { path: "../src/shared/assets/fonts/AlfaInterfaceSans-Regular.ttf", weight: "400", style: "normal" },
    { path: "../src/shared/assets/fonts/AlfaInterfaceSans-Bold.ttf", weight: "700", style: "normal" },
  ],
  variable: "--font-alfa",
  display: "swap",
});

const styrene = localFont({
  src: [
    { path: "../src/shared/assets/fonts/StyreneA-Medium-Cy-Web.ttf", weight: "500", style: "normal" },
    { path: "../src/shared/assets/fonts/StyreneA-Bold-Cy-Web.ttf", weight: "700", style: "normal" },
  ],
  variable: "--font-styrene",
  display: "swap",
});

export const metadata: Metadata = {
  title: { default: "Альфа Инсайт", template: "%s — Альфа Инсайт" },
  description: "надо придумать слоган.",
};

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="ru" suppressHydrationWarning>
      <body className={`${alfa.variable} ${styrene.variable} antialiased`}>
        <AppProviders>{children}</AppProviders>
      </body>
    </html>
  );
}
