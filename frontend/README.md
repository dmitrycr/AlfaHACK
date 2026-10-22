# Alfa Insight frontend

Фронтенд прототипа AlfaHack: Next.js App Router, React, TypeScript, Tailwind CSS и SCSS Modules.

## Запуск

Node.js 24.19.0, npm 11.9.0. Версия Node указана в `.nvmrc`.

```bash
cd alfa-insight/frontend
npm ci
npm run dev
```

Приложение доступно на http://localhost:3000. Начальная страница — заготовка рабочего пространства. Бизнес-экраны и API ещё не подключены.

## Проверки

```bash
npm run lint
npm run typecheck
npm run build
```

`npm run check` выполняет все три команды. После сборки `npm start` запускает production-сервер.

## Структура

- `app/` — маршруты и layout Next.js.
- `src/_app/` — провайдеры, глобальные стили, темы.
- `src/_pages/` — экраны приложения.
- `src/widgets/` — составные блоки экранов, по мере реализации.
- `src/features/` — пользовательские действия.
- `src/entities/` — доменные сущности, по мере реализации.
- `src/shared/` — общие компоненты, утилиты и ресурсы.

Импорты между слоями направлены вниз. ESLint проверяет направление импортов через алиас `@/`; вложенные относительные импорты и границы соседних слайсов требуют ревью. Каждый слайс экспортирует свой публичный API через `index.ts`.

## Стили и UI

Tailwind используется для сетки, размеров, отступов и адаптива. SCSS Modules — для оформления сложных компонентов. Цвета хранятся в CSS-переменных в `src/_app/styles/globals.css`. Одно свойство элемента не дублируется между Tailwind и SCSS.

Настроены светлая, тёмная и системная темы с сохранением выбора. Шрифты Alfa Interface Sans и Styrene A загружаются локально через `next/font/local`; файлы предоставлены командой для хакатона.

`components.json` задаёт алиасы для добавления компонентов shadcn/ui в `src/shared/ui`. Установлены Radix UI, Lucide, CVA, clsx и tailwind-merge. Компоненты добавляются по мере необходимости.

TanStack Query подключён для серверного состояния. Пока запросов нет. При подключении API ключи запросов должны учитывать компанию и роль. `.env.example` описывает будущий публичный адрес API. Секреты нельзя помещать в `NEXT_PUBLIC_*`.

## Docker

Из `alfa-insight/frontend`:

```bash
docker build -t alfa-insight-frontend .
docker run --rm -p 3000:3000 alfa-insight-frontend
```

Публичные переменные Next.js встраиваются при сборке. При подключении API добавьте необходимые build args или используйте серверный прокси после согласования контракта.
