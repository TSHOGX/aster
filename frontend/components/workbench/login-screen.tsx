"use client";

import { useState, type FormEvent } from "react";

import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/ui/theme-toggle";

type LoginScreenProps = {
  onSuccess: (userId: string) => void;
};

export function LoginScreen({ onSuccess }: LoginScreenProps) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (submitting) return;
    setSubmitting(true);
    setError(null);
    const { login } = await import("@/lib/api");
    const res = await login(username.trim(), password);
    setSubmitting(false);
    if (!res.ok) {
      setError(res.status === 429 ? "Too many attempts. Try again later." : res.error || "Login failed");
      setPassword("");
      return;
    }
    onSuccess(res.data.user_id);
  }

  const inputClass =
    "h-9 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 text-sm text-[var(--color-foreground)] outline-none focus-visible:border-[var(--color-accent)] focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[var(--color-accent)]";

  return (
    <div className="relative flex h-dvh items-center justify-center bg-[var(--color-background)] p-8">
      <div className="absolute right-4 top-4">
        <ThemeToggle />
      </div>
      <form
        onSubmit={handleSubmit}
        className="w-full max-w-sm rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface)] p-6"
      >
        <h1 className="text-base font-semibold">Sign in</h1>
        <p className="mt-1 text-sm text-[var(--color-foreground-muted)]">
          Enter your workbench credentials.
        </p>

        <label className="mt-5 block text-xs font-medium text-[var(--color-foreground-muted)]" htmlFor="username">
          Username
        </label>
        <input
          id="username"
          name="username"
          autoComplete="username"
          autoFocus
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          className={`mt-1 ${inputClass}`}
        />

        <label className="mt-4 block text-xs font-medium text-[var(--color-foreground-muted)]" htmlFor="password">
          Password
        </label>
        <input
          id="password"
          name="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className={`mt-1 ${inputClass}`}
        />

        {error ? (
          <p className="mt-3 text-sm text-[var(--color-danger)]" role="alert">
            {error}
          </p>
        ) : null}

        <Button
          type="submit"
          variant="primary"
          size="lg"
          className="mt-5 w-full"
          disabled={submitting || username.trim() === "" || password === ""}
        >
          {submitting ? "Signing in…" : "Sign in"}
        </Button>
      </form>
    </div>
  );
}
