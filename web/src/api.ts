export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
export type Session = {
  user: { id: string; email: string };
  workspace: { id: string; name: string };
  csrf_token: string;
  expires_at: string;
};
export async function request<T>(
  path: string,
  options: { body?: unknown; csrf?: string; signal?: AbortSignal } = {},
): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    method: options.body === undefined ? "GET" : "POST",
    credentials: "same-origin",
    signal: options.signal,
    headers: {
      ...(options.body === undefined
        ? {}
        : { "Content-Type": "application/json" }),
      ...(options.csrf ? { "X-CSRF-Token": options.csrf } : {}),
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  const payload = await response.json();
  if (!response.ok)
    throw new APIError(
      response.status,
      payload.message || "Request failed. Try again.",
    );
  return payload as T;
}
export const sessionQuery = {
  queryKey: ["session"],
  queryFn: async ({ signal }: { signal: AbortSignal }) => {
    try {
      return await request<Session>("/me", { signal });
    } catch (e) {
      if (e instanceof APIError && e.status === 401) return null;
      throw e;
    }
  },
  retry: false as const,
};
