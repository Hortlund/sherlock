import { useState, type FormEvent } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useOutletContext } from "react-router";
import { request, type Session } from "../api";
import { Field } from "../components/Field";
import { StatusBadge } from "../components/WorkspaceUI";
import { useServices, type Service } from "./Sources";

type Policy = {
  mode: "off" | "triage_only" | "auto_investigate";
  investigation_provider_id?: string;
  automatic_consent?: boolean;
  tools_enabled?: boolean;
  services: string[];
  allow_demo: boolean;
  cooldown_minutes: number;
  growth_multiplier: number;
  revision: number;
  triage_provider_id?: string;
};
type TriageProvider = {
  model?: string;
  capability?: string;
  id: string;
  kind: string;
  triage_model?: string;
  triage_capability?: string;
  enabled: boolean;
};
type Activity = {
  admission?: {
    status: string;
    reason: string;
    ai_run_id: string | null;
    investigation_id: string | null;
    state: string | null;
    run_reason: string | null;
    model: string | null;
    accounting: string | null;
    reserved: number | null;
    charged: number | null;
  };

  triage?: {
    status: string;
    reason: string;
    run_id: string | null;
    model: string | null;
    provider: string | null;
    accounting: string | null;
    reserved: number | null;
    charged: number | null;
    report: { decision: string; reason: string; evidence_ids: string[] } | null;
  };
  id: string;
  service_id: string;
  service_name: string;
  origin: string;
  state: string;
  reason: string;
  created_at: string;
  due_at: string;
  evaluated_at: string | null;
  next_eligible_at: string | null;
  policy_revision: number;
  mapping_revision: number;
  items: {
    finding_id: string;
    title: string;
    trigger: string;
    recommendation: string;
    reason: string;
    observed_at: string;
    measurement: { value: number; unit: string; minimum_delta: number };
  }[];
};
const when = (value: string) => new Date(value).toLocaleString();
const triggers: Record<string, string> = {
  new_occurrence: "New finding occurrence",
  severity_changed: "Severity changed",
  growth: "Signal increased",
  comparison_changed: "Comparison settings changed",
  "": "No material change",
};
const recommendations: Record<string, string> = {
  pending: "Waiting",
  investigate: "Investigate recommended",
  observe: "Skipped",
  need_more_data: "More data needed",
};

export function AutomationSettings() {
  const session = useOutletContext<Session>(),
    cache = useQueryClient();
  const policy = useQuery({
    queryKey: ["automation-policy"],
    queryFn: ({ signal }) => request<Policy>("/automation/policy", { signal }),
    staleTime: 0,
  });
  const services = useServices();
  const aiSettings = useQuery({
    queryKey: ["ai-settings"],
    queryFn: ({ signal }) =>
      request<{ providers: TriageProvider[] }>("/ai/settings", { signal }),
  });
  const [notice, setNotice] = useState("");
  return (
    <section className="settings-section" id="automation">
      <div className="settings-columns">
        <div className="settings-primary">
          <div className="section-heading">
            <div>
              <h2>Automation</h2>
              <p>
                Spot meaningful changes and understand what deserves a closer
                look.
              </p>
            </div>
            {policy.data ? (
              <StatusBadge
                value={policy.data.mode === "off" ? "off" : "recommended"}
              >
                {policy.data.mode === "off" ? "Off" : policy.data.mode === "auto_investigate" ? "Automatic investigations" : "Recommendations"}
              </StatusBadge>
            ) : null}
          </div>
          <p className="notification-note">
            Rules evaluate findings locally. Recommendations only keeps investigations manual.
            Automatic investigations require separate recurring consent and a tested
            investigation model. Optional AI triage helps decide which qualifying changes to investigate.
          </p>
          {policy.error || services.error ? (
            <p role="alert">{(policy.error ?? services.error)?.message}</p>
          ) : null}
          {policy.isPending || services.isPending ? (
            <p role="status">Loading automation settings…</p>
          ) : null}
          {policy.data && services.data ? (
            <PolicyForm
              key={policy.data.revision}
              policy={policy.data}
              services={services.data.items}
              providers={aiSettings.data?.providers ?? []}
              csrf={session.csrf_token}
              onSaved={(p) => {
                cache.setQueryData(["automation-policy"], p);
                void cache.invalidateQueries({
                  queryKey: ["automation-activity"],
                });
                setNotice(
                  "Automation settings saved. Existing findings establish a fresh comparison on their next observation; queued work from older settings is skipped.",
                );
              }}
            />
          ) : null}
          {notice ? <p role="status">{notice}</p> : null}
        </div>
        <aside className="context-rail">
          <h3>How recommendations work</h3>
          <ol className="checklist">
            <li>
              <strong>Detect a meaningful change</strong>
              <p>
                A new occurrence, severity change, or signal growth beyond both
                relative and absolute thresholds.
              </p>
            </li>
            <li>
              <strong>Group related findings</strong>
              <p>
                Changes for a service wait 60 seconds and share one evaluation.
              </p>
            </li>
            <li>
              <strong>Explain the decision</strong>
              <p>
                Monitoring, selected services, finding state, evidence, and
                cooldown are checked before recommending.
              </p>
            </li>
          </ol>
          <Link className="context-link" to="#monitoring">
            Review monitoring thresholds →
          </Link>
          <p className="field-hint">
            Existing notification rules keep running independently. AI advice
            cannot change findings or send alerts. Automatic investigations are off
            by default and require explicit opt-in.
          </p>
        </aside>
      </div>
      {aiSettings.error ? <p role="alert">{aiSettings.error.message}</p> : null}
      <ActivityHistory
        services={services.data?.items ?? []}
        csrf={session.csrf_token}
      />
    </section>
  );
}
function PolicyForm({
  policy,
  services,
  providers,
  csrf,
  onSaved,
}: {
  policy: Policy;
  services: Service[];
  providers: TriageProvider[];
  csrf: string;
  onSaved: (p: Policy) => void;
}) {
  const [triageProvider, setTriageProvider] = useState(
    policy.triage_provider_id ?? "",
  );
  const [mode, setMode] = useState(policy.mode);
  const [investigationProvider, setInvestigationProvider] = useState(
    policy.investigation_provider_id ?? "",
  );
  const save = useMutation({
    mutationFn: (body: Policy) =>
      request<Policy>("/automation/policy", { body, csrf }),
    onSuccess: onSaved,
  });
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    save.mutate({
      ...policy,
      mode,
      investigation_provider_id: investigationProvider,
      automatic_consent: f.has("automatic_consent"),
      tools_enabled: f.has("tools_enabled"),
      services: f.getAll("services").map(String),
      allow_demo: f.has("allow_demo"),
      triage_provider_id: f.has("triage_consent")
        ? String(f.get("triage_provider") ?? "")
        : "",
      cooldown_minutes: Number(f.get("cooldown")),
      growth_multiplier: Number(f.get("growth")),
    });
  }
  return (
    <form onSubmit={submit}>
      <label className="select-field">
        Mode
        <select
          name="mode"
          value={mode}
          onChange={(e) => setMode(e.target.value as Policy["mode"])}
        >
          <option value="off">Off — pause automation</option>
          <option value="triage_only">Recommendations only</option>
          <option value="auto_investigate">Automatic investigations</option>
        </select>
      </label>
      <fieldset>
        <legend>Automatic investigation consent</legend>
        <label className="select-field">
          Investigation provider
          <select
            name="investigation_provider"
            value={investigationProvider}
            onChange={(e) => setInvestigationProvider(e.target.value)}
            required={mode === "auto_investigate"}
          >
            <option value="">Choose a tested investigation model</option>
            {investigationProvider &&
            !providers.some((p) => p.id === investigationProvider) ? (
              <option value={investigationProvider}>
                Previously selected provider · unavailable
              </option>
            ) : null}
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.kind} · {p.model || "model not configured"} ·{" "}
                {p.capability ?? "untested"}
              </option>
            ))}
          </select>
        </label>
        <label className="checkbox-field">
          <input
            type="checkbox"
            name="automatic_consent"
            defaultChecked={policy.automatic_consent ?? false}
            required={mode === "auto_investigate"}
          />
          Allow recurring automatic investigations and paid AI analysis
        </label>
        <label className="checkbox-field"><input type="checkbox" name="tools_enabled" defaultChecked={policy.tools_enabled ?? false} />Allow bounded read-only tools for automatic investigations</label>
        <p className="field-hint">Tools require this additional opt-in. Up to 6 provider calls, 6 tool requests and 12 telemetry attempts in the original scan windows; 30,000 cumulative input / 8,000 output tokens and 180 seconds from enqueue. Shared AI budgets apply to every step. Tools cannot change scope or send messages.</p>
        <p className="field-hint">Applies only in Automatic investigations mode to the selected services. Sends the triggering scan evidence, including excerpts when allowed in AI settings. One active AI task per service; the configured cooldown also limits automatic investigations. Without tool opt-in, each analysis allows one paid attempt, up to 12,000 estimated input and 2,048 output tokens, and 180 seconds from enqueue. Daily and per-run AI budgets apply. Tools recollect only the original mapped windows.</p>
        <p className="field-hint">If triage recommends observe, needs attention, or more data, automatic analysis is skipped. Unavailable triage falls back to local rules within budget; cancelling triage stops admission. Off or Recommendations only stops future automatic dispatch. Existing findings are not replayed.</p>
        <Link to="#ai-settings">Review provider consent, payload previews and budgets →</Link>
      </fieldset>
      <fieldset>
        <legend>Selected services — none means none</legend>
        <div className="automation-service-list">
          {services.map((s) => (
            <label className="checkbox-field" key={s.id}>
              <input
                type="checkbox"
                name="services"
                value={s.id}
                defaultChecked={policy.services.includes(s.id)}
              />
              <span>
                {s.name} · {s.origin === "demo" ? "Simulated" : "Live"}
                {!s.monitoring ? " · Monitoring off" : ""}
              </span>
            </label>
          ))}
        </div>
        {services.length === 0 ? (
          <p>
            No services yet. <Link to="/services">Map a service</Link> first.
          </p>
        ) : null}
      </fieldset>
      <label className="checkbox-field">
        <input
          type="checkbox"
          name="allow_demo"
          defaultChecked={policy.allow_demo}
        />
        Include simulated demo findings
      </label>
      <div className="form-grid">
        <Field
          label="Recommendation cooldown (minutes)"
          name="cooldown"
          type="number"
          min={5}
          max={1440}
          required
          defaultValue={policy.cooldown_minutes}
        />
        <Field
          label="Minimum growth multiplier"
          name="growth"
          type="number"
          min={1.5}
          max={10}
          step={0.1}
          required
          defaultValue={policy.growth_multiplier}
        />
      </div>
      <fieldset>
        <legend>Optional AI triage</legend>
        <label className="select-field">
          Triage provider
          <select
            name="triage_provider"
            value={triageProvider}
            onChange={(e) => setTriageProvider(e.target.value)}
          >
            <option value="">Local rules only</option>
            {triageProvider &&
            !providers.some((p) => p.id === triageProvider) ? (
              <option value={triageProvider}>
                Previously selected provider · unavailable
              </option>
            ) : null}
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.kind} · {p.triage_model || "triage model not configured"} ·{" "}
                {p.triage_capability ?? "untested"}
              </option>
            ))}
          </select>
        </label>
        <label className="checkbox-field">
          <input
            type="checkbox"
            name="triage_consent"
            defaultChecked={!!policy.triage_provider_id}
          />
          Allow recurring AI triage for qualifying recommendations
        </label>
        <p className="field-hint">
          Sends compact detector values, windows and opaque service/evidence IDs
          to the selected provider, with no names or log excerpts. Provider
          service consent and demo opt-in also apply. Shares your AI budget; up
          to 4,000 estimated input and 1,000 output tokens with a 15-second
          deadline. No automatic paid retries. Local recommendations remain available
          when AI is unavailable; automatic analysis follows the mode and consent above.
        </p>
        <Link to="#ai-settings">
          Configure models, budgets and preview a triage payload sample →
        </Link>
      </fieldset>
      <p className="field-hint">
        Growth is compared with the last evaluated observation. Absolute minimum
        changes come from Monitoring settings: error-proportion increase,
        new-pattern count or warning count. Log counts are compared per minute
        of sampled data. A changed window or threshold resets the comparison.
      </p>
      <p className="field-hint">
        Saving settings invalidates older queued evaluations. Skipped changes
        are not replayed when you enable recommendations or a cooldown ends.
      </p>
      {save.error ? <p role="alert">{save.error.message}</p> : null}
      <button className="primary compact" disabled={save.isPending}>
        {save.isPending ? "Saving…" : "Save automation settings"}
      </button>
    </form>
  );
}
function ActivityHistory({
  services,
  csrf,
}: {
  services: Service[];
  csrf: string;
}) {
  const cache = useQueryClient();
  const cancel = useMutation({
    mutationFn: (id: string) =>
      request(`/ai/runs/${id}/cancel`, { body: {}, csrf }),
    onSuccess: () =>
      cache.invalidateQueries({ queryKey: ["automation-activity"] }),
  });
  const [service, setService] = useState("");
  const history = useInfiniteQuery({
    queryKey: ["automation-activity", service],
    initialPageParam: "",
    queryFn: ({ signal, pageParam }) =>
      request<{ items: Activity[]; next_cursor: string }>(
        `/automation/activity?${new URLSearchParams({ service_id: service, cursor: pageParam })}`,
        { signal },
      ),
    getNextPageParam: (p) => p.next_cursor || undefined,
    refetchInterval: 10000,
  });
  const items = history.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <section className="automation-history" aria-label="Proactive activity">
      <div className="section-heading">
        <div>
          <h2>Proactive activity</h2>
          <p>
            Recommendations and skips from meaningful finding changes. Unchanged
            scans add no new entries. History is kept for 30 days.
          </p>
        </div>
        <button
          disabled={history.isFetching}
          onClick={() => void history.refetch()}
        >
          Refresh activity
        </button>
      </div>
      <label className="select-field automation-filter">
        Filter by service
        <select value={service} onChange={(e) => setService(e.target.value)}>
          <option value="">All services</option>
          {services.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
      </label>
      {cancel.error ? <p role="alert">{cancel.error.message}</p> : null}
      {history.error ? <p role="alert">{history.error.message}</p> : null}
      {history.isPending ? <p role="status">Loading activity…</p> : null}
      {!history.isPending && !history.error && items.length === 0 ? (
        <div className="empty-state">
          <h3>No proactive activity yet</h3>
          <p>
            New findings and meaningful changes will appear here after a scan.
            Select services and enable their scheduled monitoring to receive
            recommendations.
          </p>
          <Link className="inline-link" to="/services">
            Review services →
          </Link>
        </div>
      ) : null}
      {items.map((a) => (
        <details className="automation-entry" key={a.id}>
          <summary>
            <strong>{a.service_name}</strong>
            <StatusBadge value={a.state} />
            <span>{a.origin === "demo" ? "Simulated" : "Live"}</span>
            <time>{when(a.created_at)}</time>
          </summary>
          <p>{a.reason}</p>
          {a.triage && a.triage.status !== "off" ? (
            <div className="notification-note">
              <strong>AI triage · {a.triage.status}</strong>
              {a.triage.model ? (
                <p>
                  {a.triage.provider} · {a.triage.model}
                </p>
              ) : null}
              {a.triage.report ? (
                <>
                  <p>
                    <strong>
                      AI advice: {a.triage.report.decision.replaceAll("_", " ")}
                    </strong>
                  </p>
                  <p>{a.triage.report.reason}</p>
                  <p className="field-hint">
                    The local rule decision above is unchanged. Automatic admission,
                    when enabled, is shown separately below.
                  </p>
                </>
              ) : (
                <p>
                  {a.triage.reason ||
                    "AI unavailable; local recommendation retained."}
                </p>
              )}
              {a.triage.accounting ? (
                <p className="field-hint">
                  {a.triage.accounting} · Reserved $
                  {((a.triage.reserved ?? 0) / 1e6).toFixed(6)} · Charged $
                  {((a.triage.charged ?? 0) / 1e6).toFixed(6)}
                </p>
              ) : null}
              {a.triage.run_id &&
              ["queued", "analyzing"].includes(a.triage.status) ? (
                <button
                  disabled={cancel.isPending}
                  onClick={() => cancel.mutate(a.triage!.run_id!)}
                >
                  Cancel AI triage
                </button>
              ) : null}
            </div>
          ) : null}
          {a.admission ? (
            <div className="notification-note">
              <strong>Automatic admission · {a.admission.status}</strong>
              <p>{a.admission.reason}</p>
              {a.admission.investigation_id ? (
                <Link to={`/investigations?id=${a.admission.investigation_id}`}>
                  Open automatic investigation →
                </Link>
              ) : null}
              {a.admission.state ? (
                <p>
                  AI analysis · {a.admission.state} · {a.admission.model}
                  <br />{a.admission.run_reason}
                </p>
              ) : null}
              {a.admission.accounting ? (
                <p className="field-hint">
                  {a.admission.accounting} · Reserved $
                  {((a.admission.reserved ?? 0) / 1e6).toFixed(6)} · Charged $
                  {((a.admission.charged ?? 0) / 1e6).toFixed(6)}
                </p>
              ) : null}
              {a.admission.ai_run_id &&
              ["queued", "analyzing"].includes(a.admission.state ?? "") ? (
                <button
                  disabled={cancel.isPending}
                  onClick={() => cancel.mutate(a.admission!.ai_run_id!)}
                >
                  Cancel automatic analysis
                </button>
              ) : null}
            </div>
          ) : null}
          {a.state === "pending" ? (
            <p>Eligible for evaluation after {when(a.due_at)}.</p>
          ) : null}
          {a.next_eligible_at ? (
            <p>
              Service cooldown until {when(a.next_eligible_at)}. A new
              meaningful change is still required.
            </p>
          ) : null}
          <p className="field-hint">
            Policy v{a.policy_revision} · Mapping v{a.mapping_revision}
            {a.evaluated_at ? ` · Evaluated ${when(a.evaluated_at)}` : ""}
          </p>
          <ul className="automation-findings">
            {a.items.map((i) => (
              <li key={i.finding_id}>
                <Link to={`/findings?id=${i.finding_id}`}>{i.title}</Link>
                <p>
                  <strong>
                    {recommendations[i.recommendation] ?? i.recommendation}
                  </strong>{" "}
                  · {triggers[i.trigger] ?? i.trigger}
                </p>
                <p>{i.reason}</p>
                <p className="field-hint">
                  Observed {when(i.observed_at)} ·{" "}
                  {i.measurement.unit === "error_proportion"
                    ? `${(i.measurement.value * 100).toFixed(2)}% errors`
                    : `${i.measurement.value.toFixed(3)} sampled lines/min`}
                </p>
              </li>
            ))}
          </ul>
        </details>
      ))}
      {history.hasNextPage ? (
        <button
          disabled={history.isFetching}
          onClick={() => void history.fetchNextPage()}
        >
          Load older activity
        </button>
      ) : null}
    </section>
  );
}
