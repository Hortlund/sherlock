import { useQuery } from "@tanstack/react-query";
import { Navigate, Route, Routes } from "react-router";
import { sessionQuery } from "./api";
import { AuthPage } from "./pages/AuthPage";
import { InvestigationsPage } from "./pages/Investigations";
import { FindingsPage } from "./pages/Findings";
import { ConnectionsPage, ServicesPage } from "./pages/Sources";
import { Overview, SettingsPage, Workspace } from "./pages/Workspace";
function Protected() {
  const session = useQuery(sessionQuery);
  if (session.isPending)
    return (
      <main className="loading" role="status">
        Loading your workspace…
      </main>
    );
  if (session.error)
    return (
      <main className="loading">
        <h1>Unable to connect</h1>
        <p>Check that Sherlock and PostgreSQL are running.</p>
        <button className="primary" onClick={() => void session.refetch()}>
          Try again
        </button>
      </main>
    );
  if (!session.data) return <Navigate to="/login" replace />;
  return <Workspace session={session.data} />;
}
export function App() {
  return (
    <Routes>
      <Route path="/login" element={<AuthPage />} />
      <Route path="/setup" element={<AuthPage />} />
      <Route element={<Protected />}>
        <Route index element={<Overview />} />
        <Route path="/connections" element={<ConnectionsPage />} />
        <Route path="/investigations" element={<InvestigationsPage />} />
        <Route path="/findings" element={<FindingsPage />} />
        <Route path="/services" element={<ServicesPage />} />
        <Route path="/settings" element={<SettingsPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
