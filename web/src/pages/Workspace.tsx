import { AISettings } from "./AI";
import { NotificationSettings } from "./NotificationSettings";
import { useState } from "react";
import type { FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, NavLink, Outlet, useOutletContext } from "react-router";
import {
  Search,
  ListFilter,
  ArrowUpRight,
  Database,
  Box,
  LayoutDashboard,
  LogOut,
  Settings,
  ShieldCheck,
} from "lucide-react";
import { request } from "../api";
import type { Session } from "../api";
import { OperationalSettings } from "./OperationalSettings";
import { Brand } from "../components/Brand";
import { Field } from "../components/Field";

export function Workspace({ session }: { session: Session }) {
  const cache = useQueryClient();
  const logout = useMutation({
    mutationFn: () =>
      request("/auth/logout", { body: {}, csrf: session.csrf_token }),
    onSuccess: () => {
      cache.clear();
      window.location.assign("/login");
    },
  });
  return (
    <div className="workspace-layout">
      <aside className="workspace-rail">
        <Brand />
        <div className="workspace-name">{session.workspace.name}</div>
        <nav aria-label="Main navigation">
          <NavLink to="/" end>
            <LayoutDashboard size={18} />
            Overview
          </NavLink>
          <NavLink to="/findings">
            <ListFilter size={18} />
            Findings
          </NavLink>
          <NavLink to="/investigations">
            <Search size={18} />
            Investigations
          </NavLink>
          <NavLink to="/connections">
            <Database size={18} />
            Connections
          </NavLink>
          <NavLink to="/services">
            <Box size={18} />
            Services
          </NavLink>
          <NavLink to="/settings">
            <Settings size={18} />
            Settings
          </NavLink>
        </nav>
        <div className="account">
          <span>{session.user.email}</span>
          <button
            className="text-button"
            onClick={() => logout.mutate()}
            disabled={logout.isPending}
          >
            <LogOut size={16} />
            Sign out
          </button>
          {logout.error ? (
            <p role="alert" className="error">
              {logout.error.message}
            </p>
          ) : null}
        </div>
      </aside>
      <main className="workspace-main">
        <Outlet context={session} />
      </main>
    </div>
  );
}
export function Overview() {
  const session = useOutletContext<Session>();
  const status = useQuery({
    queryKey: ["overview"],
    queryFn: () =>
      request<{
        services: number;
        monitored: number;
        active_findings: number;
        connected: number;
        sources: number;
        last_successful_scan: string | null;
        stale: boolean;
      }>("/overview"),
    refetchInterval: 10000,
  });
  const d = status.data;
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="breadcrumb">{session.workspace.name}</p>
          <h1>Overview</h1>
        </div>
        <span className="status">
          <span />
          Workspace secured
        </span>
      </div>
      {status.error ? <p className="error">{status.error.message}</p> : null}
      {!d ? (
        <p>Loading workspace…</p>
      ) : d.services === 0 ? (
        <section className="welcome-section">
          <ShieldCheck size={36} strokeWidth={1.5} />
          <h2>Your workspace is ready.</h2>
          <p>
            Your administrator account is set up. Connect Prometheus or Loki to
            begin collecting evidence.
          </p>
          <Link className="inline-link" to="/connections">
            Connect your stack <ArrowUpRight size={18} />
          </Link>
        </section>
      ) : (
        <>
          <div className="overview-stats">
            <Link to="/findings">
              <strong>{d.active_findings}</strong>
              <span>Active findings</span>
            </Link>
            <Link to="/services">
              <strong>
                {d.monitored} / {d.services}
              </strong>
              <span>Services monitored</span>
            </Link>
            <Link to="/connections">
              <strong>
                {d.connected} / {d.sources}
              </strong>
              <span>Sources passing last test</span>
            </Link>
          </div>
          <section className="welcome-section">
            <h2>
              {d.stale
                ? "Signal coverage needs attention."
                : "Monitoring your mapped services."}
            </h2>
            <p>
              {d.last_successful_scan
                ? `Latest adequate scan: ${new Date(d.last_successful_scan).toLocaleString()}`
                : "No scan has collected sufficient comparison data yet."}
            </p>
            <p>
              A passing scan means sufficient data for at least one detector.
              Review individual scan coverage for missing signals.
            </p>
            <Link className="inline-link" to="/services">
              Review coverage and run a scan →
            </Link>
          </section>
        </>
      )}
    </>
  );
}
export function SettingsPage() {
  const session = useOutletContext<Session>();
  const [done, setDone] = useState(false);
  const change = useMutation({
    mutationFn: (body: Record<string, FormDataEntryValue>) =>
      request("/auth/password", { body, csrf: session.csrf_token }),
    onSuccess: () => setDone(true),
  });
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    change.mutate(Object.fromEntries(new FormData(e.currentTarget)));
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="breadcrumb">{session.workspace.name}</p>
          <h1>Settings</h1>
        </div>
      </div>
      <section className="settings-section">
        <h2>Administrator account</h2>
        <p>{session.user.email}</p>
        {done ? (
          <div role="status">
            <h3>Password updated</h3>
            <p>All sessions have been signed out.</p>
            <a className="inline-link" href="/login">
              Sign in with your new password <ArrowUpRight size={18} />
            </a>
          </div>
        ) : (
          <form onSubmit={submit}>
            <Field
              name="current_password"
              label="Current password"
              type="password"
              autoComplete="current-password"
              required
              maxLength={256}
            />
            <Field
              name="new_password"
              label="New password"
              type="password"
              autoComplete="new-password"
              required
              minLength={12}
              maxLength={256}
              hint="At least 12 characters. Changing your password signs out all sessions."
            />
            {change.error ? (
              <p role="alert" className="error">
                {change.error.message}
              </p>
            ) : null}
            <button className="primary" disabled={change.isPending}>
              {change.isPending ? "Updating…" : "Update password"}
            </button>
          </form>
        )}
      </section>
      <NotificationSettings />
      <AISettings />
      <OperationalSettings />
    </>
  );
}
