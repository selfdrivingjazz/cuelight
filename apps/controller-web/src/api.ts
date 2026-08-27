import { createApiClient, type ApiClient, type components } from "@cable/api-client";

export const controllerBaseUrl = import.meta.env.VITE_CONTROLLER_API_URL ?? (typeof window === "undefined" ? "http://localhost/api/v1" : "/api/v1");
let currentToken: string | undefined;

export let api: ApiClient = createApiClient({ baseUrl: controllerBaseUrl });

export function setApiToken(token: string | undefined): ApiClient {
  currentToken = token;
  api = createApiClient({ baseUrl: controllerBaseUrl, token });
  return api;
}

export function getApiToken(): string | undefined { return currentToken; }
export async function getControllerHealth(): Promise<components["schemas"]["Health"]> {
  const response = await fetch("/health", { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!response.ok) throw new ApiRequestError(response.status, `Controller health returned HTTP ${response.status}.`);
  return response.json() as Promise<components["schemas"]["Health"]>;
}


export class ApiRequestError extends Error {
  constructor(public readonly status: number | undefined, message: string) {
    super(message);
    this.name = "ApiRequestError";
  }
}

export function readApiResult<T extends { data?: unknown; error?: unknown; response?: Response }>(result: T): NonNullable<T["data"]> {
  if (result.error || !result.data) {
    const problem = result.error as { detail?: string; title?: string } | undefined;
    throw new ApiRequestError(result.response?.status, problem?.detail || problem?.title || "The controller returned an unexpected response.");
  }
  return result.data as NonNullable<T["data"]>;
}

export function hasCapability(operator: components["schemas"]["OperatorMe"], capability: string, scope: string): boolean {
  return operator.capabilities[capability] === true || operator.scopes.includes(scope) || operator.scopes.includes("admin");
}
