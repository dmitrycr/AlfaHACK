import { ArrowUpRight } from "lucide-react";
import Link from "next/link";
import { ThemeSwitcher } from "@/features/change-theme";
import styles from "./home-page.module.scss";

export function HomePage() {
  return (
    <div className="mx-auto min-h-svh max-w-7xl px-6 md:px-12">
      <header className="flex flex-wrap items-center justify-between gap-5 border-b py-6">
        <Link href="/" aria-label="Альфа Инсайт — главная" className="flex items-center gap-3 text-lg font-bold">
          <span aria-hidden="true" className="flex size-10 items-center justify-center rounded-xl bg-primary text-xl text-white">А</span>
          Альфа Инсайт
        </Link>
        <ThemeSwitcher />
      </header>
      <main className="py-16 md:py-24">
        <p className="mb-5 text-sm text-muted-foreground">AlfaHack · Прототип</p>
        <h1
        className={`${styles.title} max-w-3xl font-heading text-4xl leading-tight font-medium tracking-tight md:text-6xl`}
        >События рынка.<br />Контекст вашего бизнеса.
        </h1>
        <p className="mt-7 max-w-xl text-lg leading-relaxed text-muted-foreground">Персональная аналитика, которая связывает новости с вашей компанией и помогает увидеть риски и возможности.</p>
        <section aria-labelledby="workspace-title" className={`${styles.panel} mt-12 max-w-3xl p-7 md:p-10`}>
          <div className="flex items-start justify-between gap-4">
            <h2 id="workspace-title" className="font-heading text-xl font-medium">Рабочее пространство</h2>
            <ArrowUpRight aria-hidden="true" className="size-5 text-primary" />
          </div>
          <p
  className={`${styles.description} mt-7 max-w-xl text-lg leading-relaxed text-muted-foreground`}
>Здесь появятся события, значимые для вашей компании. Подключение профиля и аналитики — на следующем этапе.</p>
        </section>
      </main>
      <footer className="border-t py-6 text-sm text-muted-foreground">Демонстрационный сервис · AlfaHack 2026</footer>
    </div>
  );
}
