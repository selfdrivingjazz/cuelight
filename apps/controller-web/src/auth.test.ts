import { afterEach, describe, expect, it, vi } from "vitest";
import { resolveSession } from "./auth";
import { getApiToken } from "./api";

const networkOperator = { id: "00000000-0000-4000-8000-7275737465ff", authMethod: "network", peerAddress: "100.101.102.103", scopes: ["node_read"], capabilities: { node_read: true } };
const tokenOperator = { id: "operator-one", tokenId: "token-one", authMethod: "token", scopes: ["node_read"], capabilities: { node_read: true } };

function mockOperatorMe(accept: (authorization: string | null) => unknown) {
  const authorizations: (string | null)[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const authorization = (input as Request).headers.get("Authorization");
    authorizations.push(authorization);
    const operator = accept(authorization);
    const body = operator || { title: "Unauthorized", status: 401, detail: "operator authentication required" };
    return new Response(JSON.stringify(body), { status: operator ? 200 : 401, headers: { "Content-Type": "application/json" } });
  });
  return { authorizations };
}

afterEach(() => vi.restoreAllMocks());

describe("operator session resolution", () => {
  it("enters without a token when control trusts this browser's network", async () => {
    const { authorizations } = mockOperatorMe((authorization) => authorization === null && networkOperator);
    const result = await resolveSession(undefined);
    expect(result.session).toEqual({ operator: networkOperator });
    expect(authorizations).toEqual([null]);
    expect(getApiToken()).toBeUndefined();
  });

  it("falls back to network access when the tab's stored token stopped working", async () => {
    const { authorizations } = mockOperatorMe((authorization) => authorization === null && networkOperator);
    const result = await resolveSession("revoked-token");
    expect(result.session?.operator.authMethod).toBe("network");
    expect(result.session?.token).toBeUndefined();
    expect(authorizations).toEqual(["Bearer revoked-token", null]);
  });

  it("keeps a working token identity even on a trusted network", async () => {
    const { authorizations } = mockOperatorMe((authorization) => (authorization === "Bearer good-token" ? tokenOperator : networkOperator));
    const result = await resolveSession("good-token");
    expect(result.session).toEqual({ operator: tokenOperator, token: "good-token" });
    expect(authorizations).toEqual(["Bearer good-token"]);
    expect(getApiToken()).toBe("good-token");
  });

  it("asks for a token when neither works, explaining a rejected stored token", async () => {
    mockOperatorMe(() => undefined);
    expect(await resolveSession(undefined)).toEqual({ error: undefined });
    expect(await resolveSession("revoked-token")).toEqual({ error: "operator authentication required" });
  });
});
