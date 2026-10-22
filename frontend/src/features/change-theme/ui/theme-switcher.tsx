"use client";

import { Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useId } from "react";

export function ThemeSwitcher() {
  const { resolvedTheme, setTheme } = useTheme();
  const tooltipId = useId();

  function handleThemeToggle(): void {
    setTheme(resolvedTheme === "dark" ? "light" : "dark");
  }

  return (
    <button
      type="button"
      onClick={handleThemeToggle}
      aria-label="Сменить тему"
      aria-describedby={tooltipId}
      className="
        group relative inline-flex size-12 shrink-0
        cursor-pointer items-center justify-center
        rounded-full border border-border
        bg-card text-foreground
        transition-[background-color,border-color,box-shadow,transform]
        duration-200
        hover:border-primary/40 hover:bg-accent hover:shadow-sm
        active:scale-95
        motion-reduce:transition-none
      "
    >
      <span
        aria-hidden="true"
        className="relative flex size-5 items-center justify-center"
      >
        <Sun
          className="
            absolute size-5 rotate-0 scale-100 opacity-100
            transition-[transform,opacity] duration-500 ease-in-out
            dark:rotate-90 dark:scale-50 dark:opacity-0
            motion-reduce:transition-none
          "
        />

        <Moon
          className="
            absolute size-5 -rotate-90 scale-50 opacity-0
            transition-[transform,opacity] duration-500 ease-in-out
            dark:rotate-0 dark:scale-100 dark:opacity-100
            motion-reduce:transition-none
          "
        />
      </span>

      <span
        id={tooltipId}
        role="tooltip"
        className="
          pointer-events-none absolute top-full right-0 z-50
          mt-3 translate-y-1 rounded-lg
          border border-border bg-popover
          px-3 py-2 text-sm whitespace-nowrap
          text-popover-foreground opacity-0 shadow-md
          transition-[opacity,transform] delay-0 duration-200
          group-hover:translate-y-0 group-hover:opacity-100
          group-hover:delay-500
          group-focus-visible:translate-y-0
          group-focus-visible:opacity-100
          group-focus-visible:delay-500
          motion-reduce:transform-none
          motion-reduce:transition-none
        "
      >
        Сменить тему
      </span>
    </button>
  );
}