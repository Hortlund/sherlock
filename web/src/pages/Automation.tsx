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
  mode: "off" | "triage_only";
  services: string[];
  allow_demo: boolean;
  cooldown_minutes: number;
  growth_multiplier: number;
  revision: number;
};
type Activity = {
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
                {policy.data.mode === "off" ? "Off" : "Recommendations"}
              </StatusBadge>
            ) : null}
          </div>
          <p className="notification-note">
            Rules evaluate findings locally. This version recommends manual
            investigation; it never starts an investigation or calls an AI
            provider.
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
            Existing notification rules keep running independently. AI model
            triage and automatic investigations are planned next.
          </p>
        </aside>
      </div>
      <ActivityHistory services={services.data?.items ?? []} />
    </section>
  );
}
function PolicyForm({
  policy,
  services,
  csrf,
  onSaved,
}: {
  policy: Policy;
  services: Service[];
  csrf: string;
  onSaved: (p: Policy) => void;
}) {
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
      mode: f.get("mode") as Policy["mode"],
      services: f.getAll("services").map(String),
      allow_demo: f.has("allow_demo"),
      cooldown_minutes: Number(f.get("cooldown")),
      growth_multiplier: Number(f.get("growth")),
    });
  }
  return (
    <form onSubmit={submit}>
      <label className="select-field">
        Mode
        <select name="mode" defaultValue={policy.mode}>
          <option value="off">Off — pause recommendations</option>
          <option value="triage_only">Recommendations only</option>
        </select>
      </label>
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
function ActivityHistory({ services }: { services: Service[] }) {
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
