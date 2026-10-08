import { expect, test } from "vitest";
import { provisionInputSchema, settingsSchema } from "./contracts";

test("provisioning requires bounded identity and secret inputs", () => {
  expect(provisionInputSchema.safeParse({ controllerUrl: "not-a-url", caPath: "", token: "short", nodeName: "" }).success).toBe(false);
  const parsed = provisionInputSchema.parse({ controllerUrl: "https://desktop.tail.example/api/v1", caPath: "/tmp/ca.pem", token: "0123456789abcdef", nodeName: "MacBook Pro" });
  expect(parsed.recover).toBe(false);
  expect(parsed.nodeName).toBe("MacBook Pro");
});

test("desktop settings preserve explicit output policy", () => {
  expect(settingsSchema.parse({})).toEqual({ autoStart: false, fullScreen: true, presentationMode: "automatic" });
  expect(settingsSchema.parse({ autoStart: true, fullScreen: false, presentationMode: "web" })).toEqual({ autoStart: true, fullScreen: false, presentationMode: "web" });
  expect(settingsSchema.safeParse({ presentationMode: "cable" }).success).toBe(false);
});
