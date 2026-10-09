import * as React from "react";
import type { components } from "@cuelight/api-client";
import { Button, Card, CardContent, CardHeader, CardTitle, ErrorState } from "@cuelight/controller-ui";
import { api, readApiResult, setApiToken } from "./api";

type Operator = components["schemas"]["OperatorMe"];
type AuthState = "checking" | "signedOut" | "authenticated" | "invalid";
interface AuthContextValue {
  token?: string;
  operator?: Operator;
  state: AuthState;
  error?: string;
  signIn: (token: string) => Promise<boolean>;
  signOut: () => void;
}
const AuthContext = React.createContext<AuthContextValue | null>(null);
const TOKEN_KEY = "cuelight.controller.operatorToken";

function readStoredToken(): string | undefined {
  if (typeof window === "undefined") return undefined;
  return window.sessionStorage.getItem(TOKEN_KEY) ?? undefined;
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const initialToken = React.useRef(readStoredToken());
  const [token, setToken] = React.useState<string | undefined>(initialToken.current);
  const [operator, setOperator] = React.useState<Operator | undefined>();
  const [state, setState] = React.useState<AuthState>("checking");
  const [error, setError] = React.useState<string>();

  const validate = React.useCallback(async (candidate: string): Promise<boolean> => {
    const normalized = candidate.trim();
    setState("checking");
    setError(undefined);
    setApiToken(normalized || undefined);
    try {
      const identity = readApiResult(await api.GET("/operator/me"));
      setToken(normalized || undefined);
      setOperator(identity);
      setState("authenticated");
      if (typeof window !== "undefined") {
        if (normalized) window.sessionStorage.setItem(TOKEN_KEY, normalized);
        else window.sessionStorage.removeItem(TOKEN_KEY);
      }
      return true;
    } catch (cause) {
      setApiToken(undefined);
      setToken(undefined);
      setOperator(undefined);
      setState(normalized ? "invalid" : "signedOut");
      setError(normalized && cause instanceof Error ? cause.message : undefined);
      if (typeof window !== "undefined") window.sessionStorage.removeItem(TOKEN_KEY);
      return false;
    }
  }, []);

  React.useEffect(() => {
    void validate(initialToken.current ?? "");
  }, [validate]);

  const signOut = React.useCallback(() => {
    initialToken.current = undefined;
    void validate("");
  }, [validate]);
  const signIn = React.useCallback((candidate: string) => validate(candidate), [validate]);
  const value = React.useMemo(() => ({ token, operator, state, error, signIn, signOut }), [token, operator, state, error, signIn, signOut]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const value = React.useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}

export function AccessScreen() {
  const auth = useAuth();
  const [candidate, setCandidate] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitting(true);
    await auth.signIn(candidate);
    setSubmitting(false);
  };
  return <main className="flex min-h-screen items-center justify-center bg-muted/30 p-4"><Card className="w-full max-w-md"><CardHeader><CardTitle>Controller access</CardTitle></CardHeader><CardContent><form className="space-y-4" onSubmit={submit}><label className="block text-sm font-medium" htmlFor="operator-token">Operator token<input id="operator-token" type="password" autoComplete="current-password" required value={candidate} onChange={(event) => setCandidate(event.currentTarget.value)} className="mt-1 h-10 w-full rounded-md border border-border bg-background px-3" /></label>{(auth.state === "invalid" || auth.error) && <ErrorState title="Access denied" description={auth.error ?? "The token is invalid."} onRetry={() => undefined} />}{auth.state === "checking" && <p role="status" className="text-sm text-muted-foreground">Validating token…</p>}<Button type="submit" disabled={submitting || !candidate.trim()}>{submitting ? "Checking…" : "Continue"}</Button></form></CardContent></Card></main>;
}
