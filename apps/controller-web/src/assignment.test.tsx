import * as React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { components } from "@cuelight/api-client";
import { ToastProvider } from "@cuelight/controller-ui";
import { NodeAssignmentComposer, remoteBody } from "./assignment";
import { queryKeys } from "./kit";

const media: components["schemas"]["Media"] = {
  id: "media-2",
  kind: "web",
  appAccess: false,
  url: "https://example.com/menu",
  title: "Menu",
  tags: [],
  version: 1,
  createdBy: "operator-1",
  updatedBy: "operator-1",
  createdAt: "2026-09-01T12:00:00.000Z",
  updatedAt: "2026-09-01T12:00:00.000Z",
};

function composer(remoteInput: boolean, showQr: boolean) {
  const client = new QueryClient();
  client.setQueryData(queryKeys.nodeAssignment("node-1"), {
    id: "assignment-1", nodeId: "node-1", mediaId: "media-1", releaseId: "release-1", releaseDigest: "a".repeat(64),
    deliveryMode: "stream", expectedGeneration: 3, desiredGeneration: 4, remoteInput, showQr,
  } satisfies components["schemas"]["Assignment"]);
  return renderToStaticMarkup(<QueryClientProvider client={client}><ToastProvider><NodeAssignmentComposer fixedNodeId="node-1" fixedMedia={media} /></ToastProvider></QueryClientProvider>);
}

const checked = (markup: string, label: string) => new RegExp(`<input[^>]*checked=""[^>]*/>${label}`).test(markup);

describe("assignment composer remote settings", () => {
  it("starts from the screen's current remote settings and keeps them on reassignment", () => {
    const on = composer(true, true);
    expect(checked(on, "Allow remote control")).toBe(true);
    expect(checked(on, "Show a remote-control QR code on the screen")).toBe(true);
    // Untouched settings are left out of the request, so the server keeps them.
    expect(remoteBody(undefined)).toEqual({});

    const off = composer(false, false);
    expect(checked(off, "Allow remote control")).toBe(false);
    expect(off).not.toContain("Show a remote-control QR code");
  });

  it("sends a changed setting explicitly, turning the QR code off with remote control", () => {
    expect(remoteBody({ remoteInput: false, showQR: true })).toEqual({ remoteInput: false, showQr: false });
    expect(remoteBody({ remoteInput: true, showQR: false })).toEqual({ remoteInput: true, showQr: false });
  });
});
