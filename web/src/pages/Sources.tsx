import { Drawer, PageHeading, StatusBadge } from "../components/WorkspaceUI";
import { useState } from "react";
import type { FormEvent } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useOutletContext } from "react-router";
import { Plus, ArrowRight } from "lucide-react";
import { request } from "../api";
import type { Session } from "../api";
import { InvestigateButton } from "./Investigations";
import { RunStatus } from "./Findings";
import { Field } from "../components/Field";

export type Connection = {
  id: string;
  name: string;
  kind: string;
  base_url: string;
  auth_mode: string;
  tenant: string;
  ca_pem: string;
  origin: string;
  enabled: boolean;
  status: string;
  last_error: string;
  last_test_at: string | null;
};
export type Mapping = {
  labels?: Record<string, string>;
  prometheus_labels?: Record<string, string>;
  loki_labels?: Record<string, string>;
  metric: string;
  error_labels: Record<string, string> | null;
  error_pattern: string;
  warning_pattern: string;
  store_excerpts: boolean;
  sensitive_patterns?: string[];
};
export type Service = {
  id: string;
  name: string;
  environment: string;
  origin: string;
  prometheus_id: string;
  loki_id: string;
  mapping: Mapping;
  revision: number;
  monitoring: boolean;
  last_scan_id?: string;
  last_scan_state?: string;
};
export type Observation = {
  kind: string;
  query: string;
  start: string;
  end: string;
  complete: boolean;
  sample_count: number;
  value: number;
  unit: string;
  errors?: { fingerprint: string; count: number; sample?: string }[];
  warnings?: { fingerprint: string; count: number; sample?: string }[];
  notes: string;
};
export type Evidence = {
  source_available?: boolean;
  id: string;
  service_id: string;
  connection_id: string;
  origin: string;
  mapping_revision: number;
  observation: Observation;
};
export function useSources() {
  return useQuery({
    queryKey: ["connections"],
    queryFn: ({ signal }) =>
      request<{ items: Connection[] }>("/connections", { signal }),
  });
}
export function useServices() {
  return useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) =>
      request<{ items: Service[]; next_cursor: string }>("/services", {
        signal,
      }),
  });
}
function message(e: unknown) {
  return e instanceof Error ? e.message : "Request failed.";
}
export const Heading = PageHeading;
export function ConnectionsPage() {
  const session = useOutletContext<Session>(),
    cache = useQueryClient(),
    navigate = useNavigate();
  const sources = useSources();
  const [editing, setEditing] = useState<Connection | null | undefined>(
      undefined,
    ),
    [kind, setKind] = useState("prometheus"),
    [mode, setMode] = useState("none"),
    [error, setError] = useState(""),
    [suggestions, setSuggestions] = useState<string[]>([]);
  const labelsQuery = useMutation({
    mutationFn: (id: string) =>
      request<{ labels: string[] }>(`/connections/${id}/labels`),
    onSuccess: (r) => setSuggestions(r.labels),
    onError: (e) => setError(message(e)),
  });
  const action = useMutation({
    mutationFn: ({ path, body }: { path: string; body: unknown }) =>
      request(path, { body, csrf: session.csrf_token }),
    onSuccess: () => cache.invalidateQueries({ queryKey: ["connections"] }),
    onError: (e) => setError(message(e)),
  });
  const save = useMutation({
    mutationFn: (body: unknown) =>
      request(`/connections${editing ? `/${editing.id}` : ""}`, {
        body,
        csrf: session.csrf_token,
      }),
    onSuccess: () => {
      setEditing(undefined);
      setError("");
      void cache.invalidateQueries({ queryKey: ["connections"] });
    },
    onError: (e) => setError(message(e)),
  });
  const demo = useMutation({
    mutationFn: () => request("/demo", { body: {}, csrf: session.csrf_token }),
    onSuccess: () => {
      void cache.invalidateQueries({ queryKey: ["connections"] });
      void cache.invalidateQueries({ queryKey: ["services"] });
      navigate("/services");
    },
    onError: (e) => setError(message(e)),
  });
  function edit(c: Connection | null) {
    setEditing(c);
    setKind(c?.kind ?? "prometheus");
    setMode(c?.auth_mode ?? "none");
    setError("");
  }
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    save.mutate(Object.fromEntries(new FormData(e.currentTarget)));
  }
  return (
    <>
      <Heading
        title="Connections"
        description="Read-only access to your existing telemetry."
      >
        <button className="primary compact" onClick={() => edit(null)}>
          <Plus size={16} />
          Add connection
        </button>
      </Heading>
      {editing === undefined && (error || sources.error) ? (
        <p role="alert" className="error">
          {error || message(sources.error)}
        </p>
      ) : null}
      {sources.isPending ? (
        <p role="status">Loading connections…</p>
      ) : sources.data?.items.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Source</th>
                <th>Status</th>
                <th>Last test</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {sources.data.items.map((c) => (
                <tr key={c.id}>
                  <td>
                    <strong>{c.name}</strong>
                    {c.origin === "demo" ? <small>Simulated</small> : null}
                    <small>{c.base_url}</small>
                  </td>
                  <td>{c.kind === "prometheus" ? "Prometheus" : "Loki"}</td>
                  <td>
                    <StatusBadge value={c.status} />
                    {c.last_error ? (
                      <small className="error-text">{c.last_error}</small>
                    ) : null}
                  </td>
                  <td>
                    {c.last_test_at
                      ? new Date(c.last_test_at).toLocaleString()
                      : "Not tested"}
                  </td>
                  <td>
                    <div className="row-actions">
                      <button
                        disabled={!c.enabled || labelsQuery.isPending}
                        onClick={() => labelsQuery.mutate(c.id)}
                      >
                        Labels
                      </button>
                      <button
                        disabled={!c.enabled || action.isPending}
                        onClick={() =>
                          action.mutate({
                            path: `/connections/${c.id}/test`,
                            body: {},
                          })
                        }
                      >
                        Test
                      </button>
                      {c.origin !== "demo" ? (
                        <button onClick={() => edit(c)}>Edit</button>
                      ) : null}
                      <button
                        disabled={action.isPending}
                        onClick={() =>
                          action.mutate({
                            path: `/connections/${c.id}/state`,
                            body: { action: c.enabled ? "disable" : "enable" },
                          })
                        }
                      >
                        {c.enabled ? "Disable" : "Enable"}
                      </button>
                      <button
                        disabled={action.isPending}
                        onClick={() => {
                          if (
                            window.confirm(
                              `Delete ${c.name}? Its stored credentials will be removed. Existing evidence follows its retention policy.`,
                            )
                          )
                            action.mutate({
                              path: `/connections/${c.id}/state`,
                              body: { action: "delete" },
                            });
                        }}
                      >
                        Delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <section className="empty-state">
          <h2>Connect your first source.</h2>
          <p>
            Add Prometheus for request metrics or Loki for application logs.
          </p>
          <button
            className="text-button inline-link"
            onClick={() => demo.mutate()}
            disabled={demo.isPending}
          >
            {demo.isPending ? "Preparing demo…" : "Try the synthetic demo"}
            <ArrowRight size={16} />
          </button>
        </section>
      )}
      {sources.data?.items.some((c) => c.origin === "demo") ? (
        <p className="table-note">
          Demo sources use synthetic telemetry. No production data is collected.
        </p>
      ) : null}
      {suggestions.length ? (
        <p className="table-note">
          Available label names (bounded suggestions): {suggestions.join(", ")}
        </p>
      ) : null}
      {editing !== undefined ? (
        <Drawer
          title={editing ? "Edit connection" : "Add a connection"}
          onClose={() => setEditing(undefined)}
        >
          <section className="editor-section">
            {error ? (
              <p role="alert" className="error">
                {error}
              </p>
            ) : null}
            <form
              className="editor-form"
              key={editing?.id ?? "new"}
              onSubmit={submit}
            >
              <Field
                name="name"
                label="Name"
                defaultValue={editing?.name ?? ""}
                maxLength={80}
                required
              />
              <label className="select-field">
                Source
                <select
                  name="kind"
                  value={kind}
                  onChange={(e) => setKind(e.target.value)}
                >
                  <option value="prometheus">Prometheus</option>
                  <option value="loki">Loki</option>
                </select>
              </label>
              <Field
                name="base_url"
                label="Base URL"
                type="url"
                placeholder="https://prometheus.example.com"
                defaultValue={editing?.base_url ?? ""}
                required
              />
              <label className="select-field">
                Authentication
                <select
                  name="auth_mode"
                  value={mode}
                  onChange={(e) => setMode(e.target.value)}
                >
                  <option value="none">None</option>
                  <option value="bearer">Bearer token</option>
                  <option value="basic">Basic authentication</option>
                </select>
              </label>
              {mode === "basic" ? (
                <Field
                  name="username"
                  label="Username"
                  autoComplete="off"
                  required={!editing}
                />
              ) : null}
              {mode !== "none" ? (
                <Field
                  name="secret"
                  label={mode === "bearer" ? "Token" : "Source password"}
                  type="password"
                  autoComplete="new-password"
                  required={!editing}
                  hint={
                    editing
                      ? "Leave blank to retain the existing credential."
                      : undefined
                  }
                />
              ) : null}
              {kind === "loki" ? (
                <Field
                  name="tenant"
                  label="Tenant (optional)"
                  defaultValue={editing?.tenant ?? ""}
                />
              ) : null}
              <details>
                <summary>Private certificate authority</summary>
                <label className="select-field">
                  CA certificate (PEM)
                  <textarea
                    name="ca_pem"
                    defaultValue={editing?.ca_pem ?? ""}
                    rows={4}
                    maxLength={8192}
                  />
                </label>
              </details>
              <p className="table-note">
                Credentials are encrypted and never returned by the API.
              </p>
              <div className="form-actions">
                <button className="primary compact" disabled={save.isPending}>
                  {save.isPending ? "Saving…" : "Save connection"}
                </button>
                <button
                  type="button"
                  className="text-button"
                  onClick={() => setEditing(undefined)}
                >
                  Cancel
                </button>
              </div>
            </form>
          </section>
        </Drawer>
      ) : null}
    </>
  );
}
function labelsText(v: Record<string, string> | null | undefined) {
  return Object.entries(v ?? {})
    .map(([k, value]) => `${k}=${value}`)
    .join(", ");
}
function labels(value: FormDataEntryValue | null) {
  const result: Record<string, string> = {};
  for (const part of String(value ?? "").split(",")) {
    const i = part.indexOf("=");
    if (i < 1)
      throw new Error("Labels must use name=value, separated by commas.");
    result[part.slice(0, i).trim()] = part.slice(i + 1).trim();
  }
  return result;
}
export function EvidenceList({ items }: { items: Evidence[] }) {
  return (
    <div className="evidence-list">
      {items.map((e) => (
        <details key={e.id}>
          <summary>
            <strong>{e.observation.kind.replaceAll("_", " ")}</strong>
            <span>
              {e.observation.kind === "log_patterns"
                ? `${e.observation.sample_count} log lines`
                : `${e.observation.value.toFixed(1)} requests`}
            </span>
            <span>
              {e.observation.complete ? "Complete result" : "Incomplete result"}
            </span>
          </summary>
          <p>
            {new Date(e.observation.start).toLocaleString()} –{" "}
            {new Date(e.observation.end).toLocaleString()} ·{" "}
            {e.origin === "demo" ? "Simulated" : "Live"} · mapping v
            {e.mapping_revision}
          </p>
          {e.source_available === false ? (
            <p>
              Source is currently unavailable. This is previously stored
              evidence.
            </p>
          ) : null}
          <code className="query">{e.observation.query}</code>
          {e.observation.notes ? <p>{e.observation.notes}</p> : null}
          {[
            ...(e.observation.errors ?? []),
            ...(e.observation.warnings ?? []),
          ].map((p) => (
            <p key={p.fingerprint}>
              <strong>{p.count} occurrences</strong> ·{" "}
              {p.sample ?? p.fingerprint}
            </p>
          ))}
          <a href={`/api/v1/evidence/${e.id}`} target="_blank" rel="noreferrer">
            Open evidence record
          </a>
        </details>
      ))}
    </div>
  );
}
export function ServicesPage() {
  const session = useOutletContext<Session>(),
    cache = useQueryClient();
  const sources = useSources(),
    services = useServices();
  const [editing, setEditing] = useState<Service | null | undefined>(undefined),
    [error, setError] = useState(""),
    [preview, setPreview] = useState<{
      evidence: Evidence[];
      issues: string[];
    } | null>(null);
  const [runID, setRunID] = useState("");
  const scan = useMutation({
    mutationFn: (id: string) =>
      request<{ id: string }>(`/services/${id}/scans`, {
        body: {},
        csrf: session.csrf_token,
      }),
    onSuccess: (r) => setRunID(r.id),
    onError: (e) => setError(message(e)),
  });
  const save = useMutation({
    mutationFn: (body: unknown) =>
      request(`/services${editing ? `/${editing.id}` : ""}`, {
        body,
        csrf: session.csrf_token,
      }),
    onSuccess: () => {
      setEditing(undefined);
      void cache.invalidateQueries({ queryKey: ["services"] });
    },
    onError: (e) => setError(message(e)),
  });
  const collect = useMutation({
    mutationFn: (id: string) =>
      request<{ evidence: Evidence[]; issues: string[] }>(
        `/services/${id}/preview`,
        { body: {}, csrf: session.csrf_token },
      ),
    onSuccess: setPreview,
    onError: (e) => setError(message(e)),
  });
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError("");
    try {
      const f = new FormData(e.currentTarget);
      save.mutate({
        name: f.get("name"),
        environment: f.get("environment"),
        prometheus_id: f.get("prometheus_id"),
        loki_id: f.get("loki_id"),
        monitoring: f.get("monitoring") === "on",
        mapping: {
          prometheus_labels: f.get("prometheus_id")
            ? labels(f.get("prometheus_labels"))
            : undefined,
          loki_labels: f.get("loki_id")
            ? labels(f.get("loki_labels"))
            : undefined,
          metric: String(f.get("metric") ?? ""),
          error_labels: f.get("error_labels")
            ? labels(f.get("error_labels"))
            : {},
          error_pattern: f.get("error_pattern"),
          warning_pattern: f.get("warning_pattern"),
          store_excerpts: f.get("store_excerpts") === "on",
          sensitive_patterns: String(f.get("sensitive_patterns") ?? "")
            .split("\n")
            .map((s) => s.trim())
            .filter(Boolean),
        },
      });
    } catch (e) {
      setError(message(e));
    }
  }
  return (
    <>
      <Heading
        title="Services"
        description="Map your sources to a service and verify signal coverage."
      >
        <button className="primary compact" onClick={() => setEditing(null)}>
          <Plus size={16} />
          Map a service
        </button>
      </Heading>
      {editing === undefined && (error || services.error) ? (
        <p role="alert" className="error">
          {error || message(services.error)}
        </p>
      ) : null}
      {services.isPending ? (
        <p role="status">Loading services…</p>
      ) : services.data?.items.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Service</th>
                <th>Configured sources</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {services.data.items.map((v) => (
                <tr key={v.id}>
                  <td>
                    <strong>{v.name}</strong>
                    <small>
                      {v.environment} ·{" "}
                      {v.origin === "demo" ? "Simulated" : "Live"} · mapping v
                      {v.revision}
                    </small>
                  </td>
                  <td>
                    {[
                      v.prometheus_id ? "Metrics" : null,
                      v.loki_id ? "Logs" : null,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                    <small>
                      {v.monitoring
                        ? "Scheduled monitoring enabled"
                        : "Manual scans only"}
                    </small>
                    {v.last_scan_id ? (
                      <button
                        className="text-button"
                        onClick={() => setRunID(v.last_scan_id!)}
                      >
                        Latest scan: {v.last_scan_state?.replaceAll("_", " ")}
                      </button>
                    ) : (
                      <small>No scans yet</small>
                    )}
                  </td>
                  <td>
                    <div className="row-actions">
                      <InvestigateButton serviceID={v.id} />
                      <button
                        disabled={scan.isPending}
                        onClick={() => scan.mutate(v.id)}
                      >
                        Run scan
                      </button>
                      <button onClick={() => setEditing(v)}>
                        Edit mapping
                      </button>
                      <button
                        disabled={collect.isPending}
                        onClick={() => {
                          setPreview(null);
                          collect.mutate(v.id);
                        }}
                      >
                        {collect.isPending ? "Collecting…" : "Preview signals"}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <section className="empty-state">
          <h2>No services mapped yet.</h2>
          <p>
            Add a source, then choose the labels that identify your service.
          </p>
        </section>
      )}
      {editing !== undefined ? (
        <Drawer
          title={editing ? "Edit service mapping" : "Map a service"}
          onClose={() => setEditing(undefined)}
        >
          <section className="editor-section">
            {error ? (
              <p role="alert" className="error">
                {error}
              </p>
            ) : null}
            <form
              className="editor-form"
              key={editing?.id ?? "new"}
              onSubmit={submit}
            >
              <Field
                name="name"
                label="Service name"
                defaultValue={editing?.name ?? ""}
                required
                maxLength={80}
              />
              <Field
                name="environment"
                label="Environment"
                defaultValue={editing?.environment ?? "production"}
                required
                maxLength={80}
              />
              {["prometheus", "loki"].map((kind) => (
                <label key={kind} className="select-field">
                  {kind === "prometheus" ? "Prometheus source" : "Loki source"}
                  <select
                    name={`${kind}_id`}
                    defaultValue={
                      kind === "prometheus"
                        ? editing?.prometheus_id
                        : editing?.loki_id
                    }
                  >
                    <option value="">Not configured</option>
                    {sources.data?.items
                      .filter((c) => c.kind === kind && c.enabled)
                      .map((c) => (
                        <option value={c.id} key={c.id}>
                          {c.name} ({c.origin})
                        </option>
                      ))}
                  </select>
                </label>
              ))}
              <Field
                name="prometheus_labels"
                label="Prometheus service labels"
                placeholder="job=checkout"
                defaultValue={labelsText(
                  editing?.mapping.prometheus_labels ??
                    editing?.mapping.labels ??
                    {},
                )}
                hint="Exact matches for the selected metrics source, separated by commas."
              />
              <Field
                name="loki_labels"
                label="Loki service labels"
                placeholder="app=checkout"
                defaultValue={labelsText(
                  editing?.mapping.loki_labels ?? editing?.mapping.labels ?? {},
                )}
                hint="Exact matches for the selected log source, separated by commas."
              />
              <Field
                name="metric"
                label="Request counter metric"
                placeholder="http_requests_total"
                defaultValue={editing?.mapping.metric ?? ""}
                hint="Required for Prometheus. Use a monotonically increasing request counter."
              />
              <Field
                name="error_labels"
                label="Error response labels"
                placeholder="status=500"
                defaultValue={
                  editing ? labelsText(editing.mapping.error_labels) : ""
                }
                hint="Additional exact-match labels selecting error requests in the same counter."
              />
              <Field
                name="error_pattern"
                label="Error log pattern"
                defaultValue={
                  editing?.mapping.error_pattern ??
                  "(?i)error|exception|timeout"
                }
                hint="Required when a Loki source is configured."
              />
              <Field
                name="warning_pattern"
                label="Warning log pattern"
                defaultValue={
                  editing?.mapping.warning_pattern ?? "(?i)warn|retry"
                }
                hint="Required when a Loki source is configured."
              />
              <label className="select-field">
                Additional sensitive log patterns (one regular expression per
                line)
                <textarea
                  name="sensitive_patterns"
                  rows={3}
                  defaultValue={
                    editing?.mapping.sensitive_patterns?.join("\n") ?? ""
                  }
                />
              </label>
              <label className="checkbox-field">
                <input
                  type="checkbox"
                  name="store_excerpts"
                  defaultChecked={editing?.mapping.store_excerpts ?? true}
                />
                Store short redacted log excerpts
              </label>
              <label className="checkbox-field">
                <input
                  type="checkbox"
                  name="monitoring"
                  defaultChecked={editing?.monitoring ?? false}
                />
                Enable scheduled monitoring
              </label>
              <div className="form-actions">
                <button className="primary compact" disabled={save.isPending}>
                  Save mapping
                </button>
                <button
                  className="text-button"
                  type="button"
                  onClick={() => setEditing(undefined)}
                >
                  Cancel
                </button>
              </div>
            </form>
          </section>
        </Drawer>
      ) : null}
      {runID ? <RunStatus id={runID} /> : null}
      {preview ? (
        <section className="preview-section">
          <h2>Signal preview</h2>
          <p>
            Bounded observations from the last complete 15-minute window.
            Configured sources do not guarantee sufficient coverage.
          </p>
          {preview.issues.map((issue, i) => (
            <p key={i} className="error">
              {issue}
            </p>
          ))}
          {preview.evidence.length ? (
            <EvidenceList items={preview.evidence} />
          ) : (
            <p>No usable evidence was collected.</p>
          )}
        </section>
      ) : null}
    </>
  );
}
