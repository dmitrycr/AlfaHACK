import Link from "next/link";

export default function NotFound() {
  return (
    <main className="mx-auto flex min-h-svh max-w-xl flex-col justify-center px-6 py-16">
      <p className="text-sm font-bold text-primary">404</p>
      <h1 className="mt-4 font-heading text-4xl font-medium">Страница не найдена</h1>
      <p className="mt-5 leading-relaxed text-muted-foreground">Проверьте адрес или вернитесь на главную страницу.</p>
      <Link href="/" className="mt-8 w-fit rounded-xl bg-primary px-6 py-3 font-bold text-primary-foreground">На главную</Link>
    </main>
  );
}
