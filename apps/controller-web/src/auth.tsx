import * as React from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { components } from "@cuelight/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, ErrorState } from "@cuelight/controller-ui";
import { api, readApiResult, setApiToken } from "./api";

type Operator = components["schemas"]["OperatorMe"];
type AuthState = "checking" | "signedOut" | "authenticated" | "invalid";
export interface OperatorSession {
  operator: Operator;
  /** Absent when control admitted this browser without a token (trusted network). */
  token?: string;
}
interface AuthContextValue {
  token?: string;
  operator?: Operator;
  state: AuthState;
  error?: string;
  signIn: (token: string) => Promise<boolean>;
  signOut: () => Promise<void>;
}
const AuthContext = React.createContext<AuthContextValue | null>(null);
const TOKEN_KEY = "cuelight.controller.operatorToken";

function readStoredToken(): string | undefined {
  if (typeof window === "undefined") return undefined;
  return window.sessionStorage.getItem(TOKEN_KEY) ?? undefined;
}

function storeToken(token: string | undefined) {
  if (typeof window === "undefined") return;
  if (token) window.sessionStorage.setItem(TOKEN_KEY, token);
  else window.sessionStorage.removeItem(TOKEN_KEY);
}

async function identify(token: string | undefined): Promise<Operator> {
  setApiToken(token);
  return readApiResult(await api.GET("/operator/me"));
}

/**
 * Resolves this browser's operator session on load: the token chosen earlier in
 * this tab first, then a token-less probe that succeeds when control trusts the
 * browser's network. `error` explains a stored token that no longer works.
 */
export async function resolveSession(storedToken: string | undefined): Promise<{ session?: OperatorSession; error?: string }> {
  let error: string | undefined;
  if (storedToken) {
    try {
      return { session: { operator: await identify(storedToken), token: storedToken } };
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "The token is invalid.";
    }
  }
  try {
    return { session: { operator: await identify(undefined) } };
  } catch {
    return { error };
  }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient();
  const [session, setSession] = React.useState<OperatorSession>();
  const [state, setState] = React.useState<AuthState>("checking");
  const [error, setError] = React.useState<string>();
  const current = React.useRef<OperatorSession | undefined>(undefined);

  const adopt = React.useCallback((next: OperatorSession | undefined, failure?: string) => {
    current.current = next;
    setSession(next);
    setError(failure);
    setState(next ? "authenticated" : failure ? "invalid" : "signedOut");
    storeToken(next?.token);
    // Cached reads belong to the previous identity and its scopes.
    void queryClient.resetQueries();
  }, [queryClient]);

  React.useEffect(() => {
    let cancelled = false;
    void resolveSession(readStoredToken()).then(({ session: resolved, error: failure }) => {
      if (!cancelled) adopt(resolved, failure);
    });
    return () => { cancelled = true; };
  }, [adopt]);

  const signIn = React.useCallback(async (candidate: string): Promise<boolean> => {
    const token = candidate.trim();
    if (!token) return false;
    try {
      adopt({ operator: await identify(token), token });
      return true;
    } catch (cause) {
      // A rejected token never ends a working session (e.g. trusted-network access).
      setApiToken(current.current?.token);
      setError(cause instanceof Error ? cause.message : "The token is invalid.");
      if (!current.current) setState("invalid");
      return false;
    }
  }, [adopt]);

  const signOut = React.useCallback(async () => {
    const { session: resolved } = await resolveSession(undefined);
    adopt(resolved);
  }, [adopt]);

  const value = React.useMemo(() => ({ token: session?.token, operator: session?.operator, state, error, signIn, signOut }), [session, state, error, signIn, signOut]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const value = React.useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}

/** Token entry; also offered in Settings to trusted-network operators who want a token identity. */
export function TokenForm({ submitLabel = "Continue" }: { submitLabel?: string }) {
  const auth = useAuth();
  const [candidate, setCandidate] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitting(true);
    if (await auth.signIn(candidate)) setCandidate("");
    setSubmitting(false);
  };
  return <form className="space-y-4" onSubmit={submit}><label className="block text-sm font-medium" htmlFor="operator-token">Operator token<input id="operator-token" type="password" autoComplete="current-password" required value={candidate} onChange={(event) => setCandidate(event.currentTarget.value)} className="mt-1 h-10 w-full rounded-md border border-border bg-background px-3" /></label>{auth.error && <ErrorState title="Access denied" description={auth.error} onRetry={() => undefined} />}<Button type="submit" disabled={submitting || !candidate.trim()}>{submitting ? "Checking…" : submitLabel}</Button></form>;
}

export function AccessScreen() {
  return <main className="flex min-h-screen items-center justify-center bg-muted/30 p-4"><Card className="w-full max-w-md"><CardHeader><CardTitle>Controller access</CardTitle></CardHeader><CardContent><TokenForm /></CardContent></Card></main>;
}
