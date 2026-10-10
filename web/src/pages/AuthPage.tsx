import { useState } from "react";
import type { FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Navigate, useNavigate } from "react-router";
import { ArrowRight, LockKeyhole } from "lucide-react";
import { request, sessionQuery } from "../api";
import { AuthLayout } from "../components/AuthLayout";
import { Field } from "../components/Field";

export function AuthPage() {
  const navigate = useNavigate();
  const cache = useQueryClient();
  const [error, setError] = useState("");
  const status = useQuery({
    queryKey: ["setup-status"],
    queryFn: ({ signal }) =>
      request<{ initialized: boolean }>("/setup/status", { signal }),
    retry: false,
  });
  const session = useQuery(sessionQuery);
  const setup = status.data?.initialized === false;
  const mutation = useMutation({
    mutationFn: (data: Record<string, FormDataEntryValue>) =>
      request(setup ? "/setup/bootstrap" : "/auth/login", { body: data }),
    onSuccess: async () => {
      await cache.invalidateQueries({ queryKey: ["session"] });
      await cache.invalidateQueries({ queryKey: ["setup-status"] });
      navigate("/", { replace: true });
    },
    onError: (e: Error) => setError(e.message),
  });
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError("");
    mutation.mutate(Object.fromEntries(new FormData(e.currentTarget)));
  }
  if (session.data) return <Navigate to="/" replace />;
  if (status.isPending || session.isPending)
    return (
      <AuthLayout>
        <p role="status">Loading your workspace…</p>
      </AuthLayout>
    );
  if (status.error || session.error)
    return (
      <AuthLayout>
        <h1>Unable to connect</h1>
        <p className="subtitle">
          Sherlock could not reach its database. Check that the application and
          PostgreSQL are running.
        </p>
        <button
          className="primary"
          onClick={() => {
            void status.refetch();
            void session.refetch();
          }}
        >
          Try again
        </button>
      </AuthLayout>
    );
  return (
    <AuthLayout setup={setup}>
      <h1>{setup ? "Create your workspace" : "Welcome back"}</h1>
      <p className="subtitle">
        {setup
          ? "Set up the administrator account for this installation."
          : "Sign in to your Sherlock workspace."}
      </p>
      <form onSubmit={submit}>
        {setup ? (
          <>
            <Field
              name="setup_token"
              label="Setup token"
              type="password"
              placeholder="Installation setup token"
              autoComplete="off"
              required
              hint="Find this in your local .env file."
            />
            <Field
              name="workspace_name"
              label="Workspace name"
              defaultValue="My workspace"
              maxLength={80}
              required
              autoComplete="organization"
            />
          </>
        ) : null}
        <Field
          name="email"
          label="Email"
          type="email"
          placeholder="you@example.com"
          autoComplete="username"
          maxLength={254}
          required
        />
        <Field
          name="password"
          label="Password"
          type="password"
          placeholder={setup ? "At least 12 characters" : "Your password"}
          autoComplete={setup ? "new-password" : "current-password"}
          minLength={setup ? 12 : undefined}
          maxLength={256}
          required
        />
        {error ? (
          <p className="error" role="alert">
            {error}
          </p>
        ) : null}
        <button className="primary" disabled={mutation.isPending}>
          {mutation.isPending
            ? "Please wait…"
            : setup
              ? "Create workspace"
              : "Sign in"}
          <ArrowRight size={20} aria-hidden="true" />
        </button>
      </form>
      <p className="privacy-note">
        <LockKeyhole size={17} aria-hidden="true" />
        Credentials stay on your server.
      </p>
    </AuthLayout>
  );
}
