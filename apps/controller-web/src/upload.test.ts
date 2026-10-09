import { afterEach, expect, test, vi } from "vitest";
import { setApiToken } from "./api";
import { uploadSelectedFile } from "./upload";

afterEach(() => {
  vi.unstubAllGlobals();
  setApiToken(undefined);
});

test("streams selected file bytes before completing the upload", async () => {
  const requests: Request[] = [];
  const fetcher = vi.fn(async (request: RequestInfo | URL, init?: RequestInit) => {
    const value = request instanceof Request ? request : new Request(request, init);
    requests.push(value);
    const url = new URL(value.url, "http://controller.test");
    if (value.method === "POST" && url.pathname.endsWith("/uploads")) {
      return Response.json({ id: "upload-one", name: "shell.tar", length: 4, offset: 0, mediaType: "application/x-tar", state: "open" }, { status: 201 });
    }
    if (value.method === "PATCH" && url.pathname.endsWith("/uploads/upload-one")) {
      return Response.json({ id: "upload-one", name: "shell.tar", length: 4, offset: 4, mediaType: "application/x-tar", state: "open" }, { headers: { "Upload-Offset": "4" } });
    }
    if (value.method === "POST" && url.pathname.endsWith("/uploads/upload-one/complete")) {
      return Response.json({ id: "asset-one", name: "shell.tar", artifact: { digest: "a".repeat(64), size: 4, mediaType: "application/x-tar", state: "ready" } });
    }
    return Response.json({ title: "Unexpected request" }, { status: 500 });
  });
  vi.stubGlobal("fetch", fetcher);
  setApiToken("operator-token");

  const file = new File([new Uint8Array([1, 2, 3, 4])], "shell.tar", { type: "application/x-tar" });
  const asset = await uploadSelectedFile(file);

  expect(asset.artifact.digest).toBe("a".repeat(64));
  const patch = requests.find((request) => request.method === "PATCH");
  expect(patch).toBeDefined();
  expect([...new Uint8Array(await patch!.arrayBuffer())]).toEqual([1, 2, 3, 4]);
  expect(patch!.headers.get("Upload-Offset")).toBe("0");
  expect(patch!.headers.get("Authorization")).toBe("Bearer operator-token");
});
