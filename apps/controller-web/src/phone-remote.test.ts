import { expect, test } from "vitest";
import { remotePostStatus } from "./phone-remote";

test("an ended or disabled remote session asks for a fresh scan; other failures stay connection errors", async () => {
  expect(await remotePostStatus(new Response(null, { status: 202 }))).toEqual({ state: "ready" });
  for (const status of [401, 403, 404, 409, 410]) {
    expect(await remotePostStatus(Response.json({ status, detail: "remote input is disabled" }, { status }))).toEqual({ state: "ended", detail: "remote input is disabled" });
  }
  expect(await remotePostStatus(new Response("gone", { status: 410 }))).toEqual({ state: "ended" });
  expect(await remotePostStatus(Response.json({ status: 503, detail: "unavailable" }, { status: 503 }))).toEqual({ state: "error" });
});
