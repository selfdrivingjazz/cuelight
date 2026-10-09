import { describe, expect, test } from "vitest";
import { JoinLinkError, normalizeSHA256, parseJoinLink } from "./join-link";
import { compareVersions } from "./version";

const pin = "ab".repeat(32);
const token = "c3VwZXItc2VjcmV0LXRva2Vu+/=";

function link(query: Record<string, string>): string {
  return `cuelight://join?${new URLSearchParams(query).toString()}`;
}

describe("parseJoinLink", () => {
  test("accepts a full link and decodes every field", () => {
    expect(parseJoinLink(link({ v: "1", url: "https://mars.lan:8443/", token, ca: `sha256:${pin.toUpperCase()}`, name: "Mars College" }))).toEqual({
      controlUrl: "https://mars.lan:8443",
      token,
      caSha256: pin,
      name: "Mars College",
    });
  });

  test("publicly trusted controls omit the pin; name falls back to the host", () => {
    expect(parseJoinLink(link({ v: "1", url: "https://desk.tail1234.ts.net", token }))).toEqual({ controlUrl: "https://desk.tail1234.ts.net", token, caSha256: undefined, name: "desk.tail1234.ts.net" });
  });

  test("tolerates launcher variants of the scheme shape", () => {
    const query = new URLSearchParams({ v: "1", url: "https://a.example", token }).toString();
    expect(parseJoinLink(`cuelight:join?${query}`).controlUrl).toBe("https://a.example");
    expect(parseJoinLink(`cuelight://join/?${query}`).controlUrl).toBe("https://a.example");
  });

  test.each([
    ["other scheme", `https://join?${new URLSearchParams({ v: "1", url: "https://a.example", token })}`],
    ["other action", `cuelight://leave?${new URLSearchParams({ v: "1", url: "https://a.example", token })}`],
    ["future version", link({ v: "2", url: "https://a.example", token })],
    ["missing version", link({ url: "https://a.example", token })],
    ["plain http control", link({ v: "1", url: "http://a.example", token })],
    ["credentials in control URL", link({ v: "1", url: "https://user:pw@a.example", token })],
    ["query in control URL", link({ v: "1", url: "https://a.example/?x=1", token })],
    ["short token", link({ v: "1", url: "https://a.example", token: "short" })],
    ["malformed pin", link({ v: "1", url: "https://a.example", token, ca: "sha1:abcd" })],
    ["truncated pin", link({ v: "1", url: "https://a.example", token, ca: `sha256:${pin.slice(2)}` })],
    ["garbage", "not a link"],
  ])("rejects %s", (_name, value) => {
    expect(() => parseJoinLink(value)).toThrow(JoinLinkError);
  });
});

test("normalizeSHA256 accepts openssl colon fingerprints", () => {
  const colon = pin.toUpperCase().match(/../g)!.join(":");
  expect(normalizeSHA256(colon)).toBe(pin);
  expect(normalizeSHA256(`sha256:${pin}`)).toBe(pin);
  expect(normalizeSHA256("abc")).toBeUndefined();
});

test("compareVersions orders prereleases before their release", () => {
  expect(compareVersions("0.3.0-rc.2", "0.3.0-rc.10")).toBe(-1);
  expect(compareVersions("0.3.0-rc.10", "0.3.0")).toBe(-1);
  expect(compareVersions("0.10.0", "0.9.9")).toBe(1);
  expect(compareVersions("1.0.0", "v1.0.0")).toBe(0);
});
