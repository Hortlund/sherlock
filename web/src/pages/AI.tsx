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
      <h2>AI providers</h2>
      <p>
        Analyze existing investigations with OpenAI or Anthropic. Service data
        is sent only when workspace processing, a provider, and the selected
        service are enabled. Synthetic capability tests send no service
        telemetry.
      </p>
      {data.fixture ? (
        <p className="notification-note">
          SIMULATED PROVIDER FIXTURE — responses and costs are test data. No
          cloud calls.
        </p>
      ) : null}
      <p>
        Model IDs and prices are your configuration assumptions. Use your
        provider's current standard input/output prices, in USD per million
        tokens. API usage is separate from chat subscriptions. A successful
        synthetic test checks this model's response contract, not analysis
        accuracy.
      </p>
      <div className="row-actions">
        <a
          href="https://developers.openai.com/api/docs/pricing"
          target="_blank"
          rel="noreferrer"
        >
          OpenAI pricing
        </a>
        <a
          href="https://platform.claude.com/docs/en/about-claude/pricing"
          target="_blank"
          rel="noreferrer"
        >
          Anthropic pricing
        </a>
      </div>
      <PolicyForm
        key={data.policy.revision}
        policy={data.policy}
        csrf={session.csrf_token}
        onSaved={refresh}
      />
      <p>
        Today's accounted usage: {money(data.spent_today)} · Outstanding
        reservations: {money(data.reserved)}. UTC daily reset; uncertain
        reservations carry forward until reconciled.
      </p>
      <p>
        Admission uses a conservative input estimate and a 2,048-token output
        ceiling, including reported reasoning usage. One provider call per run;
        interrupted calls are not automatically replayed. Your provider's
        invoice may differ from configured estimates.
      </p>
      <button className="text-button" onClick={() => setEditing(null)}>
        Add AI provider
      </button>
      {data.providers.map((p) => (
        <article className="ai-provider" key={p.id}>
          <h3>
            {p.kind === "openai" ? "OpenAI" : "Anthropic"} · {p.model}
          </h3>
          <p>
            {p.enabled ? "Enabled" : "Disabled"} · Capability: {p.capability} ·
            Encrypted key stored · {p.services.length} allowed service(s)
          </p>
          <p>
            {p.allow_demo ? "Demo allowed" : "Live only"} ·{" "}
            {p.allow_excerpts
              ? "Redacted excerpts allowed where service mapping permits"
              : "Counts only; no excerpts"}
          </p>
          <div className="row-actions">
            <button onClick={() => setEditing(p)}>Edit {p.kind}</button>
            <button
              disabled={!p.enabled || !data.policy.enabled || action.isPending}
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
        <ProviderForm
          key={editing?.id ?? "new"}
          provider={editing}
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
      ) : null}
      {notice ? <p role="status">{notice}</p> : null}
      {action.error || services.error || runs.error ? (
        <p role="alert" className="error">
          {(action.error || services.error || runs.error)?.message}
        </p>
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
    <form className="notification-form" onSubmit={submit}>
      <h3>Workspace consent and budgets</h3>
      <label className="checkbox-field">
        <input type="checkbox" name="enabled" defaultChecked={policy.enabled} />
        Allow external AI processing for explicitly selected services
      </label>
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
      <p>
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
  services,
  csrf,
  onSaved,
  onCancel,
}: {
  provider: Provider | null;
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
      <h3>{provider ? "Edit AI provider" : "New AI provider"}</h3>
      <label className="select-field">
        Provider
        <select
          aria-label="AI provider"
          name="kind"
          defaultValue={provider?.kind ?? "openai"}
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
        · <strong>{run.state}</strong> ·{" "}
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
  const previewGeneration = useRef(0);
  const previewAction = useMutation({
    mutationFn: ({ providerID }: { providerID: string; generation: number }) =>
      request<Preview>(`/investigations/${investigationID}/ai/preview`, {
        body: { provider_id: providerID },
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
          fresh,
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
        One bounded analysis of this investigation's existing evidence. It makes
        suggestions; it does not collect new data or establish a root cause.
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
          <button
            className="text-button"
            disabled={!providerID || previewAction.isPending}
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
              The deterministic report above remains available. AI content may
              be unavailable, deleted, expired, or withheld.
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
