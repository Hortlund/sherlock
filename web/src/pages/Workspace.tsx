import { AISettings } from "./AI";
import { AutomationSettings } from "./Automation";
import { NotificationSettings } from "./NotificationSettings";
import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Link,
  NavLink,
  Outlet,
  useLocation,
  useOutletContext,
} from "react-router";
import {
  Search,
  ListFilter,
  ArrowUpRight,
  ArrowRight,
  ChevronRight,
  Database,
  Box,
  LayoutDashboard,
  LogOut,
  Settings,
  ShieldCheck,
  Menu,
  X,
} from "lucide-react";
import { request } from "../api";
import type { Session } from "../api";
import type { Finding } from "./Findings";
import { OperationalSettings, SystemSettings } from "./OperationalSettings";
import { Brand } from "../components/Brand";
import { Field } from "../components/Field";
import { PageHeading, StatusBadge } from "../components/WorkspaceUI";

const navigation = [
  {
    label: "Workspace",
    items: [
      { to: "/", label: "Overview", icon: LayoutDashboard },
      { to: "/findings", label: "Findings", icon: ListFilter },
      { to: "/investigations", label: "Investigations", icon: Search },
    ],
  },
  {
    label: "Configuration",
    items: [
      { to: "/services", label: "Services", icon: Box },
      { to: "/connections", label: "Connections", icon: Database },
      { to: "/settings", label: "Settings", icon: Settings },
    ],
  },
];
export function Workspace({ session }: { session: Session }) {
  const cache = useQueryClient(),
    location = useLocation();
  const [menuOpen, setMenuOpen] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  const section =
    navigation
      .flatMap((g) => g.items)
      .find((item) => item.to === location.pathname)?.label ?? "Workspace";
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
      <a
        href="#main-content"
        className="skip-link"
        onClick={(event) => {
          event.preventDefault();
          document.getElementById("main-content")?.focus();
        }}
      >
        Skip to content
      </a>
      <aside
        className={`workspace-rail ${menuOpen ? "menu-open" : ""}`}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            setMenuOpen(false);
            menuButton.current?.focus();
          }
        }}
      >
        <div className="rail-brand">
          <Brand />
          <button
            ref={menuButton}
            type="button"
            className="icon-button mobile-menu"
            aria-label={menuOpen ? "Close navigation" : "Open navigation"}
            aria-expanded={menuOpen}
            aria-controls="workspace-navigation"
            onClick={() => setMenuOpen((v) => !v)}
          >
            {menuOpen ? <X size={20} /> : <Menu size={20} />}
          </button>
        </div>
        <div className="workspace-name">{session.workspace.name}</div>
        <nav id="workspace-navigation" aria-label="Main navigation">
          {navigation.map((group) => (
            <div className="nav-group" key={group.label}>
              <p className="nav-group-label">{group.label}</p>
              {group.items.map(({ to, label, icon: Icon }) => (
                <NavLink
                  key={to}
                  to={to}
                  end={to === "/"}
                  onClick={() => setMenuOpen(false)}
                >
                  <Icon size={18} strokeWidth={1.7} />
                  <span>{label}</span>
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
        <div className="account">
          <span>{session.user.email}</span>
          <button
            className="text-button"
            onClick={() => logout.mutate()}
            disabled={logout.isPending}
          >
            <LogOut size={15} />
            Sign out
          </button>
          {logout.error ? (
            <p role="alert" className="error">
              {logout.error.message}
            </p>
          ) : null}
        </div>
      </aside>
      <main className="workspace-main" id="main-content" tabIndex={-1}>
        <div className="workspace-breadcrumb">
          <span>Workspace</span>
          <ChevronRight size={13} />
          <span>{section}</span>
        </div>
        <Outlet context={session} />
      </main>
    </div>
  );
}
export function Overview() {
  const status = useQuery({
    queryKey: ["overview"],
    staleTime: 0,
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
  const recent = useQuery({
    queryKey: ["findings", "", ""],
    staleTime: 0,
    queryFn: () =>
      request<{ items: Finding[]; next_cursor: string }>(
        "/findings?state=&cursor=",
      ),
    refetchInterval: 10000,
  });
  const d = status.data;
  return (
    <>
      <PageHeading
        title="Overview"
        description="Your services, findings, and the next step."
      >
        <Link className="primary compact" to="/investigations">
          Investigate a service <ArrowRight size={16} />
        </Link>
      </PageHeading>
      {status.error ? (
        <p role="alert" className="error">
          {status.error.message}
        </p>
      ) : null}
      {!d ? (
        <p role="status">Loading workspace…</p>
      ) : d.services === 0 ? (
        <section className="welcome-section">
          <ShieldCheck size={32} strokeWidth={1.5} />
          <h2>Your workspace is ready.</h2>
          <p>
            Connect Prometheus or Loki, map a service, and collect your first
            evidence. You can also explore with simulated telemetry.
          </p>
          <Link className="primary compact" to="/connections">
            Connect your stack <ArrowRight size={16} />
          </Link>
          <ol className="onboarding-steps">
            <li>
              <strong>Connect a source</strong>
              <span>Use existing metrics and logs, or try the demo.</span>
            </li>
            <li>
              <strong>Map a service</strong>
              <span>Choose the signals that belong to your service.</span>
            </li>
            <li>
              <strong>Run your first scan</strong>
              <span>Review findings and investigate the evidence.</span>
            </li>
          </ol>
        </section>
      ) : (
        <>
          <div className="overview-stats">
            <Link to="/findings">
              <span>Active findings</span>
              <strong>{d.active_findings}</strong>
              <small>
                Review observed changes <ArrowUpRight size={14} />
              </small>
            </Link>
            <Link to="/services">
              <span>Services monitored</span>
              <strong>
                {d.monitored}
                <em> / {d.services}</em>
              </strong>
              <small>
                Manage service coverage <ArrowUpRight size={14} />
              </small>
            </Link>
            <Link to="/connections">
              <span>Sources passing last test</span>
              <strong>
                {d.connected}
                <em> / {d.sources}</em>
              </strong>
              <small>
                Review connections <ArrowUpRight size={14} />
              </small>
            </Link>
          </div>
          <div className="overview-columns">
            <section className="recent-findings">
              <div className="section-heading">
                <h2>Observed findings</h2>
                <Link className="inline-link" to="/findings">
                  View all <ArrowRight size={15} />
                </Link>
              </div>
              {recent.error ? (
                <p role="alert" className="error">
                  {recent.error.message}
                </p>
              ) : recent.isPending ? (
                <p role="status">Loading findings…</p>
              ) : recent.data.items.length ? (
                recent.data.items.slice(0, 5).map((f) => (
                  <Link
                    className="recent-finding"
                    key={f.id}
                    to={`/findings?id=${f.id}`}
                  >
                    <div>
                      <div className="finding-meta">
                        <StatusBadge value={f.severity} />
                        <span>
                          {f.origin === "demo" ? "Simulated" : "Live"}
                        </span>
                        <span>{f.state}</span>
                      </div>
                      <h3>{f.title}</h3>
                      <time>{new Date(f.last_seen).toLocaleString()}</time>
                    </div>
                    <ChevronRight size={18} />
                  </Link>
                ))
              ) : (
                <div className="empty-state">
                  <h3>No findings yet</h3>
                  <p>
                    Run a scan to compare recent activity with your baseline.
                  </p>
                  <Link className="inline-link" to="/services">
                    Review services <ArrowRight size={15} />
                  </Link>
                </div>
              )}
            </section>
            <aside className="context-rail">
              <h3>
                {d.stale ? "Coverage needs attention" : "Signal coverage"}
              </h3>
              <p>
                {d.last_successful_scan
                  ? `Latest adequate scan: ${new Date(d.last_successful_scan).toLocaleString()}`
                  : "No scan has collected sufficient comparison data yet."}
              </p>
              <p>
                A passing scan confirms enough data for at least one detector.
                Check each service for missing signals.
              </p>
              <Link className="inline-link" to="/services">
                Review coverage <ArrowRight size={15} />
              </Link>
              <div className="context-section">
                <h3>Connected workflows</h3>
                <Link className="context-link" to="/settings#notifications">
                  Set up notifications <ArrowRight size={14} />
                </Link>
                <Link className="context-link" to="/settings#ai-settings">
                  Configure AI analysis <ArrowRight size={14} />
                </Link>
              </div>
            </aside>
          </div>
        </>
      )}
    </>
  );
}
const settingsSections = [
  { id: "account", label: "Account" },
  { id: "monitoring", label: "Monitoring" },
  { id: "ai-settings", label: "AI providers" },
  { id: "automation", label: "Automation" },
  { id: "notifications", label: "Notifications" },
  { id: "system", label: "System" },
];
export function SettingsPage() {
  const { hash } = useLocation();
  const selected = settingsSections.some((s) => `#${s.id}` === hash)
    ? hash.slice(1)
    : "account";
  return (
    <>
      <PageHeading
        title="Settings"
        description="Manage your workspace, integrations, and data."
      />
      <nav className="section-navigation" aria-label="Settings sections">
        {settingsSections.map((s) => (
          <Link
            key={s.id}
            to={`#${s.id}`}
            aria-current={selected === s.id ? "page" : undefined}
          >
            {s.label}
          </Link>
        ))}
      </nav>
      <div className="settings-content" key={selected}>
        {selected === "account" ? (
          <div className="settings-columns">
            <AccountSettings />
            <aside className="context-rail">
              <h3>Your workspace</h3>
              <p>
                This installation has one administrator. A password change signs
                out all active sessions.
              </p>
              <div className="context-section">
                <h3>Configure Sherlock</h3>
                <Link className="context-link" to="#monitoring">
                  Monitoring and retention <ArrowRight size={14} />
                </Link>
                <Link className="context-link" to="#ai-settings">
                  Optional AI analysis <ArrowRight size={14} />
                </Link>
                <Link className="context-link" to="#notifications">
                  Alert destinations <ArrowRight size={14} />
                </Link>
              </div>
            </aside>
          </div>
        ) : null}
        {selected === "monitoring" ? <OperationalSettings /> : null}
        {selected === "ai-settings" ? <AISettings /> : null}
        {selected === "automation" ? <AutomationSettings /> : null}
        {selected === "notifications" ? <NotificationSettings /> : null}
        {selected === "system" ? <SystemSettings /> : null}
      </div>
    </>
  );
}
function AccountSettings() {
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
            <button className="primary compact" disabled={change.isPending}>
              {change.isPending ? "Updating…" : "Update password"}
            </button>
          </form>
        )}
      </section>
    </>
  );
}
