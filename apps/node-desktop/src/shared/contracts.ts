import { z } from "zod";

export const provisionInputSchema = z.object({
  controllerUrl: z.string().url(),
  caPath: z.string().min(1),
  token: z.string().min(16),
  nodeName: z.string().trim().min(1).max(120),
  recover: z.boolean().default(false),
});
export type ProvisionInput = z.infer<typeof provisionInputSchema>;

export const settingsSchema = z.object({
  autoStart: z.boolean().default(false),
  fullScreen: z.boolean().default(true),
  presentationMode: z.enum(["automatic", "media", "media-schedule", "web"]).default("automatic"),
});
export type DesktopSettings = z.infer<typeof settingsSchema>;

export interface ProvisionedNode {
  version: number;
  nodeId: string;
  nodeName: string;
  controlUrl: string;
  eventEpoch: string;
  publisherKeyId: string;
  caPath: string;
  certificatePath: string;
  privateKeyPath: string;
  publisherPublicKeyPath: string;
}

export interface DesktopStatus {
  phase: "unprovisioned" | "stopped" | "starting" | "running" | "error";
  node?: ProvisionedNode;
  playerPid?: number;
  outputWindows: number;
  rendererUrl?: string;
  lastError?: string;
  settings: DesktopSettings;
  logPath: string;
}

export interface CuelightNodeDesktopAPI {
  status(): Promise<DesktopStatus>;
  chooseCA(): Promise<string | undefined>;
  provision(input: ProvisionInput): Promise<DesktopStatus>;
  start(): Promise<DesktopStatus>;
  stop(): Promise<DesktopStatus>;
  forget(): Promise<DesktopStatus>;
  updateSettings(settings: DesktopSettings): Promise<DesktopStatus>;
  showOutput(): Promise<void>;
  showControls(): Promise<void>;
  openLogs(): Promise<void>;
  onStatus(listener: (status: DesktopStatus) => void): () => void;
}

declare global {
  interface Window {
    cuelightNode: CuelightNodeDesktopAPI;
  }
}
