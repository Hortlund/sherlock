import type { ReactNode } from "react";
import { Brand } from "./Brand";
export function AuthLayout({
  children,
  setup = false,
}: {
  children: ReactNode;
  setup?: boolean;
}) {
  return (
    <div className="auth-layout">
      <aside className="auth-rail">
        <Brand />
        <div className="rail-content">
          <h2>
            Your stack.
            <br />
            Your evidence.
          </h2>
          <p>Reliability investigations, hosted by you.</p>
          {setup ? (
            <ol className="setup-steps" aria-label="Setup progress">
              {[
                "Secure workspace",
                "Connect a source",
                "Map a service",
                "Start monitoring",
              ].map((step, i) => (
                <li key={step} aria-current={i === 0 ? "step" : undefined}>
                  <span className="step-number">{i + 1}</span>
                  {step}
                </li>
              ))}
            </ol>
          ) : null}
        </div>
        <p className="rail-footer">Read-only by design</p>
      </aside>
      <main className="auth-main">
        <span className="deployment-label">SELF-HOSTED</span>
        <div className="auth-form-container">{children}</div>
      </main>
    </div>
  );
}
