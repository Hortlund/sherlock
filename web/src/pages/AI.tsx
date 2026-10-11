import { Plus, ArrowRight } from "lucide-react";
import { Drawer, StatusBadge } from "../components/WorkspaceUI";
import { useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useOutletContext } from "react-router";
import { request } from "../api";
import type { Session } from "../api";
import { Field } from "../components/Field";
import { StoredEvidence } from "./Findings";

type Policy = {
  enabled: boolean;
  daily_limit: number;
  run_limit: number;
  revision: number;
};
type Provider = {
  id: string;
  kind: string;
  revision: number;
  enabled: boolean;
  model: string;
  capability: string;
  triage_model?: string;
  triage_capability?: string;
  triage_input_price?: number;
  triage_output_price?: number;
  services: string[];
  allow_demo: boolean;
  allow_excerpts: boolean;
  input_price: number;
  output_price: number;
  price_date: string;
};
type Settings = {
  policy: Policy;
  providers: Provider[];
  spent_today: number;
  reserved: number;
  fixture: boolean;
};
type AIReport = {
  summary: string;
  observations: { text: string; evidence_ids: string[] }[];
  hypotheses: {
    text: string;
    supporting_ids: string[];
    contradicting_ids: string[];
  }[];
  unknowns: string[];
  checks: string[];
};
type AIRun = {
 deep?: boolean;
 tools?: boolean;
 parent_ai_run_id?: string;
 question?: string;
 telemetry_attempts?: number;
  id: string;
  provider_id: string;
  investigation_id: string;
  purpose: string;
  model: string;
  state: string;
  reason: string;
  accounting: string;
  reserved: number;
  charged: number;
  input_tokens: number | null;
  output_tokens: number | null;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  expires_at: string;
  report: AIReport | null;
};
type Preview = {
  payload: unknown;
  preview_digest: string;
  provider_id: string;
  model: string;
  reservation: number;
  input_bound: number;
  output_limit: number;
  expires_at: string;
};
const money = (v: number) => `$${(v / 1e6).toFixed(6)}`;
const active = (v: AIRun) => ["queued", "analyzing"].includes(v.state);
const settingsQuery = {
  queryKey: ["ai-settings"],
  queryFn: ({ signal }: { signal: AbortSignal }) =>
    request<Settings>("/ai/settings", { signal }),
};
function useAIRuns(investigation?: string) {
  const query = useInfiniteQuery({
    queryKey: ["ai-runs", investigation ?? ""],
    initialPageParam: "",
    queryFn: ({ signal, pageParam }) => {
      const params = new URLSearchParams({ cursor: pageParam });
      if (investigation) params.set("investigation_id", investigation);
      return request<{ items: AIRun[]; next_cursor: string }>(
        `/ai/runs?${params}`,
        { signal },
      );
    },
    getNextPageParam: (page) => page.next_cursor || undefined,
    refetchInterval: (q) =>
      q.state.data?.pages.some((page) => page.items.some(active))
        ? 1000
        : false,
  });
  return {
    ...query,
    data: query.data
      ? { items: query.data.pages.flatMap((page) => page.items) }
      : undefined,
  };
}
function MoreAIRuns({ runs }: { runs: ReturnType<typeof useAIRuns> }) {
  return runs.hasNextPage ? (
    <button
      className="text-button"
      disabled={runs.isFetching}
      onClick={() => void runs.fetchNextPage()}
    >
      {runs.isFetchingNextPage
        ? "Loading older AI runs…"
        : "Load older AI runs"}
    </button>
  ) : null;
}
export function AISettings() {
  const session = useOutletContext<Session>(),
    cache = useQueryClient();
  const settings = useQuery(settingsQuery),
    runs = useAIRuns();
  const services = useQuery({
    queryKey: ["services"],
    queryFn: ({ signal }) =>
      request<{ items: { id: string; name: string; origin: string }[] }>(
        "/services",
        { signal },
      ),
  });
  const [editing, setEditing] = useState<Provider | null | undefined>();
  const [notice, setNotice] = useState("");
  const [triagePreview, setTriagePreview] = useState<unknown>();
  const sample = useMutation({
    mutationFn: (id: string) =>
      request<{ payload: unknown }>(`/ai/providers/${id}/triage-preview`, {
        body: {},
        csrf: session.csrf_token,
      }),
    onSuccess: (v) => setTriagePreview(v.payload),
  });
  const [newKind, setNewKind] = useState("openai");
  const refresh = () => {
    void cache.invalidateQueries({ queryKey: ["ai-settings"] });
    void cache.invalidateQueries({ queryKey: ["ai-runs"] });
  };
  const action = useMutation({
    mutationFn: ({ path, body }: { path: string; body: unknown }) =>
      request(path, { body, csrf: session.csrf_token }),
    onSuccess: (_, { path }) => {
      refresh();
      setNotice(
        path.endsWith("/test")
          ? "Synthetic capability test queued. This can incur API charges; inspect its result below."
          : "AI settings updated.",
      );
    },
  });
  if (settings.isPending)
    return (
      <section className="settings-section">
        <p>Loading AI settings…</p>
      </section>
    );
  if (settings.error) return <p role="alert">{settings.error.message}</p>;
  const data = settings.data;
  return (
    <section className="settings-section ai-settings" id="ai-settings">
      <div className="settings-columns">
        <div className="settings-primary">
          <div className="section-heading">
            <div>
              <h2>AI providers</h2>
              <p>Choose where your investigation evidence can be analyzed.</p>
            </div>
            <button
              className="primary compact"
              onClick={() => setEditing(null)}
            >
              <Plus size={16} />
              Add AI provider
            </button>
          </div>
          {data.fixture ? (
            <p className="notification-note">
              SIMULATED PROVIDER FIXTURE — responses and costs are test data. No
              cloud calls.
            </p>
          ) : null}
          {data.providers.length === 0 ? (
            <div className="provider-options">
              {["OpenAI", "Anthropic"].map((name) => (
                <div className="provider-option" key={name}>
                  <div>
                    <h3>{name}</h3>
                    <p>Not configured</p>
                  </div>
                  <button
                    onClick={() => {
                      setNewKind(name.toLowerCase());
                      setEditing(null);
                    }}
                  >
                    Configure {name}
                  </button>
                </div>
              ))}
            </div>
          ) : null}
          {data.providers.map((p) => (
            <article className="ai-provider" key={p.id}>
              <h3>
                {p.kind === "openai" ? "OpenAI" : "Anthropic"} · {p.model}
              </h3>
              <p>
                {p.enabled ? "Enabled" : "Disabled"} · Capability:{" "}
                {p.capability} · Encrypted key stored · {p.services.length}{" "}
                allowed service(s)
              </p>
              <p>
                {p.allow_demo ? "Demo allowed" : "Live only"} ·{" "}
                {p.allow_excerpts
                  ? "Redacted excerpts allowed where service mapping permits"
                  : "Counts only; no excerpts"}
              </p>
              <div className="row-actions">
                <button onClick={() => setEditing(p)}>Edit {p.kind}</button>
                {p.triage_model ? (
                  <>
                    <span>
                      Triage: {p.triage_model} ·{" "}
                      {p.triage_capability ?? "untested"}
                    </span>
                    <button
                      disabled={sample.isPending}
                      onClick={() => sample.mutate(p.id)}
                    >
                      Preview {p.kind} triage sample
                    </button>
                    <button
                      disabled={
                        !p.enabled || !data.policy.enabled || action.isPending
                      }
                      onClick={() => {
                        if (
                          window.confirm(
                            `Send a synthetic triage test to ${p.kind}/${p.triage_model}? This can incur API charges within your budget; no service data is included.`,
                          )
                        )
                          action.mutate({
                            path: `/ai/providers/${p.id}/triage-test`,
                            body: { confirm: true },
                          });
                      }}
                    >
                      Test {p.kind} triage
                    </button>
                  </>
                ) : null}
                <button
                  disabled={
                    !p.enabled || !data.policy.enabled || action.isPending
                  }
                  onClick={() => {
                    if (
                      window.confirm(
                        `Send a synthetic capability test to ${p.kind}/${p.model}? It can incur API charges within your budget. No service telemetry is included.`,
                      )
                    )
                      action.mutate({
                        path: `/ai/providers/${p.id}/test`,
                        body: { confirm: true },
                      });
                  }}
                >
                  Test {p.kind}
                </button>
                <button
                  disabled={action.isPending}
                  onClick={() => {
                    if (
                      window.confirm(
                        "Delete this provider key and local generated content? Pending work is cancelled; transmitted data cannot be recalled.",
                      )
                    )
                      action.mutate({
                        path: `/ai/providers/${p.id}/delete`,
                        body: {},
                      });
                  }}
                >
                  Delete {p.kind}
                </button>
              </div>
            </article>
          ))}
          {editing !== undefined && services.data ? (
            <Drawer
              title={editing ? "Edit AI provider" : "Add AI provider"}
              onClose={() => setEditing(undefined)}
            >
              <ProviderForm
                key={editing?.id ?? "new"}
                provider={editing}
                initialKind={newKind}
                services={services.data.items}
                csrf={session.csrf_token}
                onCancel={() => setEditing(undefined)}
                onSaved={() => {
                  setEditing(undefined);
                  refresh();
                  setNotice(
                    "Provider saved. Run a synthetic test before analyzing evidence. Edits cancel pending work and clear earlier AI content for this provider.",
                  );
                }}
              />
            </Drawer>
          ) : null}
          <PolicyForm
            key={data.policy.revision}
            policy={data.policy}
            csrf={session.csrf_token}
            onSaved={refresh}
          />
          {notice ? <p role="status">{notice}</p> : null}
          {action.error || services.error || runs.error ? (
            <p role="alert" className="error">
              {(action.error || services.error || runs.error)?.message}
            </p>
          ) : null}
          {sample.error ? <p role="alert">{sample.error.message}</p> : null}
          {triagePreview ? (
            <details open>
              <summary>Triage payload sample · no data sent</summary>
              <p>
                Real triage uses the same format with eligible signal numbers
                and evidence IDs. No log excerpts or service names are sent.
              </p>
              <pre>{JSON.stringify(triagePreview, null, 2)}</pre>
            </details>
          ) : null}
          <h3>AI usage and capability tests</h3>
          <button className="text-button" onClick={refresh}>
            Refresh AI status
          </button>
          {runs.data?.items.map((run) => (
            <RunHistory key={run.id} run={run} />
          ))}
          <MoreAIRuns runs={runs} />
          {runs.data?.items.length === 0 ? <p>No AI calls yet.</p> : null}
        </div>
        <aside className="context-rail">
          <h3>Usage today</h3>
          <p className="usage-amount">{money(data.spent_today)}</p>
          <p>
            Reserved{" "}
            <strong className="usage-reserved">{money(data.reserved)}</strong>
          </p>
          <p className="field-hint">
            UTC daily reset. Uncertain reservations carry forward until
            reconciled.
          </p>
          <div className="context-section">
            <h3>Before you begin</h3>
            <ol className="checklist">
              <li>
                <strong>Enable processing</strong>
                <p>Allow external AI processing and set a budget.</p>
              </li>
              <li>
                <strong>Choose allowed services</strong>
                <p>
                  Configure a provider and select which services can share
                  evidence.
                </p>
              </li>
              <li>
                <strong>Test your model</strong>
                <p>Run a synthetic capability test before an investigation.</p>
              </li>
            </ol>
          </div>
          <details className="help-disclosure">
            <summary>Data sharing and limits</summary>
            <p>
              Only explicitly allowed service evidence is sent. Model IDs and
              prices are your configuration assumptions; API usage is separate
              from chat subscriptions.
            </p>
            <p>
              Admission estimates input and allows up to 2,048 output tokens.
              Interrupted calls are not automatically replayed. Your provider's
              invoice may differ.
            </p>
            <a
              className="context-link"
              href="https://developers.openai.com/api/docs/pricing"
              target="_blank"
              rel="noreferrer"
            >
              OpenAI pricing <ArrowRight size={14} />
            </a>
            <a
              className="context-link"
              href="https://platform.claude.com/docs/en/about-claude/pricing"
              target="_blank"
              rel="noreferrer"
            >
              Anthropic pricing <ArrowRight size={14} />
            </a>
          </details>
        </aside>
      </div>
    </section>
  );
}
function PolicyForm({
  policy,
  csrf,
  onSaved,
}: {
  policy: Policy;
  csrf: string;
  onSaved: () => void;
}) {
  const save = useMutation({
    mutationFn: (body: unknown) => request("/ai/policy", { body, csrf }),
    onSuccess: onSaved,
  });
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    save.mutate({
      revision: policy.revision,
      enabled: f.has("enabled"),
      daily_limit: Math.round(Number(f.get("daily")) * 1e6),
      run_limit: Math.round(Number(f.get("run")) * 1e6),
    });
  }
  return (
    <form className="notification-form policy-form" onSubmit={submit}>
      <h3>Workspace consent and budgets</h3>
      <label className="checkbox-field">
        <input type="checkbox" name="enabled" defaultChecked={policy.enabled} />
        Allow external AI processing for explicitly selected services
      </label>
      <div className="form-grid">
        <Field
          label="Daily AI budget (USD)"
          name="daily"
          type="number"
          min="0"
          max="1000"
          step="0.000001"
          required
          defaultValue={policy.daily_limit / 1e6}
        />
        <Field
          label="Per-run AI budget (USD)"
          name="run"
          type="number"
          min="0"
          max="1000"
          step="0.000001"
          required
          defaultValue={policy.run_limit / 1e6}
        />
      </div>
      <p className="field-hint">
        Changing budgets cancels queued/in-flight work. Disabling also erases
        local AI content. Provider retention follows your account terms;
        transmitted data cannot be recalled.
      </p>
      {save.error ? <p role="alert">{save.error.message}</p> : null}
      <button className="primary compact" disabled={save.isPending}>
        Save AI policy
      </button>
    </form>
  );
}
function ProviderForm({
  provider,
  initialKind = "openai",
  services,
  csrf,
  onSaved,
  onCancel,
}: {
  provider: Provider | null;
  initialKind?: string;
  services: { id: string; name: string; origin: string }[];
  csrf: string;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const save = useMutation({
    mutationFn: (body: unknown) =>
      request(`/ai/providers${provider ? `/${provider.id}` : ""}`, {
        body,
        csrf,
      }),
    onSuccess: onSaved,
  });
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    save.mutate({
      api_key: f.get("key"),
      provider: {
        kind: provider?.kind ?? f.get("kind"),
        model: f.get("model"),
        triage_model: f.get("triage_model"),
        triage_input_price: Math.round(
          Number(f.get("triage_input_price")) * 1e6,
        ),
        triage_output_price: Math.round(
          Number(f.get("triage_output_price")) * 1e6,
        ),
        revision: provider?.revision ?? 0,
        enabled: f.has("enabled"),
        services: f.getAll("services"),
        allow_demo: f.has("demo"),
        allow_excerpts: f.has("excerpts"),
        input_price: Math.round(Number(f.get("input_price")) * 1e6),
        output_price: Math.round(Number(f.get("output_price")) * 1e6),
        price_date: f.get("price_date"),
      },
    });
  }
  return (
    <form className="notification-form" onSubmit={submit}>
      <label className="select-field">
        Provider
        <select
          aria-label="AI provider"
          name="kind"
          defaultValue={provider?.kind ?? initialKind}
          disabled={!!provider}
        >
          <option value="openai">OpenAI</option>
          <option value="anthropic">Anthropic</option>
        </select>
      </label>
      <Field
        label="Analysis model ID"
        name="model"
        required
        maxLength={120}
        defaultValue={provider?.model ?? ""}
        hint="Enter an exact model ID available to your API account that supports structured output."
      />
      <Field
        label="Provider API key"
        name="key"
        type="password"
        autoComplete="new-password"
        required={!provider}
        maxLength={1024}
        hint="Encrypted and write-only. Leave blank when editing to retain the stored key."
      />
      <Field
        label="Input price (USD per million tokens)"
        name="input_price"
        type="number"
        min="0"
        max="10000"
        step="0.000001"
        required
        defaultValue={(provider?.input_price ?? 0) / 1e6}
      />
      <Field
        label="Output price (USD per million tokens)"
        name="output_price"
        type="number"
        min="0"
        max="10000"
        step="0.000001"
        required
        defaultValue={(provider?.output_price ?? 0) / 1e6}
      />
      <fieldset>
        <legend>Optional triage model</legend>
        <p>
          Use a separate model for compact proactive assessments. Configure
          prices, run its synthetic test, then opt in under Automation.
          Automatic investigations require separate opt-in in Automation settings.
        </p>
        <Field
          label="Triage model ID"
          name="triage_model"
          maxLength={120}
          defaultValue={provider?.triage_model ?? ""}
        />
        <Field
          label="Triage input price (USD per million tokens)"
          name="triage_input_price"
          type="number"
          min="0"
          max="10000"
          step="0.000001"
          defaultValue={(provider?.triage_input_price ?? 0) / 1e6}
        />
        <Field
          label="Triage output price (USD per million tokens)"
          name="triage_output_price"
          type="number"
          min="0"
          max="10000"
          step="0.000001"
          defaultValue={(provider?.triage_output_price ?? 0) / 1e6}
        />
      </fieldset>
      <Field
        label="Price effective date"
        name="price_date"
        type="date"
        required
        defaultValue={
          provider?.price_date ?? new Date().toISOString().slice(0, 10)
        }
      />
      <fieldset>
        <legend>Services allowed to send data (none means none)</legend>
        {services.map((v) => (
          <label key={v.id} className="checkbox-field">
            <input
              type="checkbox"
              name="services"
              value={v.id}
              defaultChecked={provider?.services.includes(v.id) ?? false}
            />
            {v.name} ({v.origin})
          </label>
        ))}
      </fieldset>
      <label className="checkbox-field">
        <input
          type="checkbox"
          name="demo"
          defaultChecked={provider?.allow_demo ?? false}
        />
        Allow simulated demo evidence to leave this installation
      </label>
      <label className="checkbox-field">
        <input
          type="checkbox"
          name="excerpts"
          defaultChecked={provider?.allow_excerpts ?? false}
        />
        Allow bounded redacted log excerpts where the service stores excerpts
      </label>
      <label className="checkbox-field">
        <input
          type="checkbox"
          name="enabled"
          defaultChecked={provider?.enabled ?? false}
        />
        Enable this AI provider
      </label>
      <p>
        Counts, timestamps and excerpts may be sensitive. Preview the exact
        analysis request before sending it. No labels, source URLs, credentials
        or raw upstream responses are sent.
      </p>
      {save.error ? <p role="alert">{save.error.message}</p> : null}
      <div className="form-actions">
        <button className="primary compact" disabled={save.isPending}>
          Save AI provider
        </button>
        <button className="text-button" type="button" onClick={onCancel}>
          Cancel provider editing
        </button>
      </div>
    </form>
  );
}
function RunHistory({ run }: { run: AIRun }) {
  const session = useOutletContext<Session>(),
    cache = useQueryClient();
  const action = useMutation({
    mutationFn: ({ suffix, body }: { suffix: string; body: unknown }) =>
      request(`/ai/runs/${run.id}/${suffix}`, {
        body,
        csrf: session.csrf_token,
      }),
    onSuccess: () => {
      void cache.invalidateQueries({ queryKey: ["ai-runs"] });
      void cache.invalidateQueries({ queryKey: ["ai-settings"] });
    },
  });
  return (
    <details className="ai-history">
      <summary>
        {run.model} ·{" "}
        {run.purpose === "test"
          ? "Synthetic capability test"
          : "Evidence analysis"}{" "}
        · <StatusBadge value={run.state} /> ·{" "}
        {new Date(run.created_at).toLocaleString()}
      </summary>
      <p>{run.reason}</p>
      <p>
        {run.accounting === "unknown" || run.accounting === "reserved"
          ? `Reserved ${money(run.reserved)}`
          : `Accounted ${money(run.charged)}`}{" "}
        · {run.input_tokens ?? "Unknown"} input /{" "}
        {run.output_tokens ?? "unknown"} output tokens · {run.accounting}
      </p>
      <p>
        Content expires {new Date(run.expires_at).toLocaleString()}. One bounded
        provider attempt.
      </p>
      {active(run) ? (
        <button
          className="text-button"
          disabled={action.isPending}
          onClick={() => action.mutate({ suffix: "cancel", body: {} })}
        >
          Cancel AI analysis
        </button>
      ) : null}
      {run.accounting === "unknown" && !active(run) ? (
        <button
          className="text-button"
          onClick={() => {
            const value = window.prompt(
              "After verifying this request in your provider account, enter its actual USD charge. This releases the uncertain reservation. Available four minutes after dispatch.",
            );
            if (
              value !== null &&
              value.trim() !== "" &&
              Number.isFinite(Number(value)) &&
              Number(value) >= 0
            )
              action.mutate({
                suffix: "reconcile",
                body: { confirm: true, cost: Math.round(Number(value) * 1e6) },
              });
          }}
        >
          Reconcile verified provider charge
        </button>
      ) : null}
      {run.report ? (
        <button
          className="text-button"
          onClick={() => {
            if (
              window.confirm(
                "Delete this local AI report and copied payload? Usage history remains.",
              )
            )
              action.mutate({ suffix: "delete", body: {} });
          }}
        >
          Delete AI content
        </button>
      ) : null}
      {action.error ? <p role="alert">{action.error.message}</p> : null}
    </details>
  );
}
export function AIAnalysis({
  investigationID,
  serviceID,
}: {
  investigationID: string;
  serviceID: string;
}) {
  const session = useOutletContext<Session>(),
    cache = useQueryClient();
  const settings = useQuery(settingsQuery),
    runs = useAIRuns(investigationID);
  const [providerID, setProvider] = useState("");
  const [selectedRun, setSelectedRun] = useState<string | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [expanded, setExpanded] = useState<string[] | null>(null);
  const [fresh, setFresh] = useState(false);
  const [tools, setTools] = useState(false);
  const [parent, setParent] = useState("");
  const [question, setQuestion] = useState("");
  const previewGeneration = useRef(0);
  const previewAction = useMutation({
    mutationFn: ({ providerID }: { providerID: string; generation: number }) =>
      request<Preview>(`/investigations/${investigationID}/ai/preview`, {
        body: { provider_id: providerID, tools, parent_ai_run_id: parent, question },
        csrf: session.csrf_token,
      }),
    onSuccess: (result, variables) => {
      if (
        variables.generation === previewGeneration.current &&
        result.provider_id === variables.providerID
      )
        setPreview(result);
    },
  });
  const launch = useMutation({
    mutationFn: () => {
      if (!preview || preview.provider_id !== providerID)
        throw new Error("Preview the selected provider before sending.");
      return request<{ id: string }>(`/investigations/${investigationID}/ai`, {
        body: {
          provider_id: preview.provider_id,
          preview_digest: preview.preview_digest,
          confirm: true,
          fresh, tools, parent_ai_run_id: parent, question,
        },
        csrf: session.csrf_token,
      });
    },
    onSuccess: (result) => {
      setSelectedRun(result.id);
      setPreview(null);
      void cache.invalidateQueries({ queryKey: ["ai-runs"] });
      void cache.invalidateQueries({ queryKey: ["ai-settings"] });
    },
  });
  const choices =
    settings.data?.providers?.filter(
      (p) => p.enabled && p.services.includes(serviceID),
    ) ?? [];
  const latest =
    runs.data?.items.find((r) => r.id === selectedRun) ?? runs.data?.items[0];
  return (
    <section className="report-section ai-analysis">
      <p className="eyebrow">Optional AI assistance</p>
      <h2>Investigate with AI</h2>
      <p>
        Analyze existing evidence or explicitly allow bounded read-only collection.
        Results are suggestions; citations do not establish a root cause.
      </p>
      {settings.data?.fixture ? (
        <p>SIMULATED PROVIDER FIXTURE — no cloud calls.</p>
      ) : null}
      {!settings.data?.policy?.enabled || choices.length === 0 ? (
        <p>
          <Link to="/settings#ai-settings">
            Configure AI consent, a provider, and this service in Settings.
          </Link>
        </p>
      ) : (
        <>
          <label className="select-field">
            Analysis provider
            <select
              aria-label="Analysis provider"
              value={providerID}
              onChange={(e) => {
                previewGeneration.current++;
                setProvider(e.target.value);
                setPreview(null);
                previewAction.reset();
              }}
            >
              <option value="">Choose provider and model</option>
              {choices.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.kind} · {p.model} ({p.capability})
                </option>
              ))}
            </select>
          </label>
          <label className="checkbox-field">
            <input type="checkbox" checked={tools} onChange={(e) => { setTools(e.target.checked); setPreview(null); previewGeneration.current++; }} />
            Allow bounded read-only investigation tools
          </label>
          {tools ? <p className="field-hint">Uses only mapped Prometheus counts, Loki patterns and retained evidence in the original incident and comparison windows. Up to 6 provider calls, 6 tool requests, 12 telemetry attempts, 30,000 input and 8,000 output tokens, and 180 seconds from enqueue. Each step must fit the per-run and daily cost limits. No automatic paid retries.</p> : null}
          {parent ? <div className="notification-note">
            <label>Follow-up question<textarea aria-label="Follow-up question" maxLength={512} value={question} onChange={(e) => { setQuestion(e.target.value); setPreview(null); previewGeneration.current++; }} /></label>
            <p>Same service and original windows. Uses up to three recent question/answer summaries and retained evidence; expires within 24 hours. Without tools, this is one provider call. Enabling tools starts a linked bounded run. For a wider scope, start a new investigation.</p>
            <button onClick={() => {setParent("");setQuestion("");setPreview(null);previewGeneration.current++;}}>Cancel follow-up</button>
          </div> : null}
          <button
            className="text-button"
            disabled={!providerID || previewAction.isPending || !!parent && !question.trim()}
            onClick={() =>
              previewAction.mutate({
                providerID,
                generation: ++previewGeneration.current,
              })
            }
          >
            Preview AI payload
          </button>
        </>
      )}
      {preview && preview.provider_id === providerID ? (
        <div className="notification-preview">
          <h3>Review data before sending</h3>
          <p>
            {preview.model} · Admission estimate: {preview.input_bound} input
            tokens and {preview.output_limit} output tokens · Reservation{" "}
            {money(preview.reservation)}. This can incur API charges.
          </p>
          <pre>{JSON.stringify(preview.payload, null, 2)}</pre>
          <label className="checkbox-field">
            <input
              type="checkbox"
              checked={fresh}
              onChange={(e) => setFresh(e.target.checked)}
            />
            Request fresh analysis even if an eligible saved result exists (may
            incur another charge)
          </label>
          <button
            className="primary compact"
            disabled={launch.isPending || runs.data?.items.some(active)}
            onClick={() => launch.mutate()}
          >
            Send to AI provider
          </button>
          <button
            className="text-button"
            onClick={() => {
              previewGeneration.current++;
              setPreview(null);
            }}
          >
            Close AI preview
          </button>
        </div>
      ) : null}
      {previewAction.error || launch.error || settings.error || runs.error ? (
        <p role="alert" className="error">
          {
            (
              previewAction.error ||
              launch.error ||
              settings.error ||
              runs.error
            )?.message
          }
        </p>
      ) : null}
      {latest ? (
        <>
          <RunHistory run={latest} />
          {latest.question ? <p><strong>Question:</strong> {latest.question}</p> : null}
          {latest.deep ? <DeepProgress key={latest.id} run={latest} /> : null}
          {latest.report ? <button onClick={() => {setParent(latest.id);setProvider(latest.provider_id);setQuestion("");setTools(false);setPreview(null);previewGeneration.current++;}}>Ask a follow-up</button> : null}
          {active(latest) ? (
            <p role="status">
              {latest.state === "queued"
                ? "AI analysis queued."
                : "Provider is analyzing the bounded evidence."}
            </p>
          ) : null}
          {latest.report ? (
            <div className="ai-report">
              <h3>AI analysis</h3>
              <p>{latest.report.summary}</p>
              <p>
                Citations establish evidence scope, not the truth of a
                hypothesis. Validate suggested causes independently.
              </p>
              <h4>Observed evidence</h4>
              {latest.report.observations.map((o, i) => (
                <div key={i}>
                  <p>{o.text}</p>
                  <button
                    className="text-button"
                    onClick={() => setExpanded(o.evidence_ids)}
                  >
                    Inspect AI evidence ({o.evidence_ids.length})
                  </button>
                </div>
              ))}
              <h4>Hypotheses to test</h4>
              {latest.report.hypotheses.map((h, i) => (
                <div key={i}>
                  <p>{h.text}</p>
                  <button
                    className="text-button"
                    onClick={() =>
                      setExpanded([...h.supporting_ids, ...h.contradicting_ids])
                    }
                  >
                    Supporting: {h.supporting_ids.length} · Contradicting:{" "}
                    {h.contradicting_ids.length}
                  </button>
                </div>
              ))}
              <h4>Unknowns</h4>
              <ul>
                {latest.report.unknowns.map((v, i) => (
                  <li key={i}>{v}</li>
                ))}
              </ul>
              <h4>Manual checks</h4>
              <ol>
                {latest.report.checks.map((v, i) => (
                  <li key={i}>{v}</li>
                ))}
              </ol>
            </div>
          ) : !active(latest) ? (
            <p>
              The deterministic report remains available in the Report tab. AI
              content may be unavailable, deleted, expired, or withheld.
            </p>
          ) : null}
        </>
      ) : null}
      {expanded ? (
        <div>
          <h3>AI supporting evidence</h3>
          <StoredEvidence ids={expanded} />
        </div>
      ) : null}
      {runs.data && runs.data.items.length > 1 ? (
        <details>
          <summary>Other AI attempts ({runs.data.items.length - 1})</summary>
          {runs.data.items
            .filter((v) => v.id !== latest?.id)
            .map((v) => (
              <RunHistory key={v.id} run={v} />
            ))}
          <MoreAIRuns runs={runs} />
        </details>
      ) : null}
    </section>
  );
}

function DeepProgress({ run }: { run: AIRun }) {
  const [evidence, setEvidence] = useState<string[] | null>(null);
  const steps = useQuery({
    queryKey: ["ai-steps", run.id, run.state],
    queryFn: ({ signal }) => request<{
      items: { sequence: number; kind: string; state: string; reason: string;
        reserved: number; charged: number; evidence_ids: string[] }[]
    }>(`/ai/runs/${run.id}/steps`, { signal }),
    refetchInterval: active(run) ? 1000 : false,
  });
  return <div className="notification-note">
    <h3>Investigation steps</h3>
    <p>{run.telemetry_attempts ?? 0} / 12 telemetry attempts · {money(run.charged)} charged</p>
    {steps.error ? <p role="alert">{steps.error.message}</p> : null}
    <ol>{steps.data?.items.map(step => <li key={step.sequence}>
      <strong>{step.kind} · {step.state}</strong>
      <p>{step.reason}</p>
      <p className="field-hint">Reserved {money(step.reserved)} · Charged {money(step.charged)}</p>
      {step.evidence_ids.length ? <button className="text-button" onClick={() => setEvidence(step.evidence_ids)}>Inspect tool evidence ({step.evidence_ids.length})</button> : null}
    </li>)}</ol>
    {evidence ? <div><h4>Collected tool evidence</h4><StoredEvidence ids={evidence} /></div> : null}
  </div>;
}
