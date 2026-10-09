import { z } from "zod";

export const membershipIdSchema = z.string().regex(/^[a-z0-9][a-z0-9-]{0,62}$/);

/** `shared` follows the primary display; a number pins an Electron display id. */
export const displayBindingSchema = z.union([z.literal("shared"), z.number().int()]);
export type DisplayBinding = z.infer<typeof displayBindingSchema>;

export const membershipSchema = z.object({
  id: membershipIdSchema,
  name: z.string().trim().min(1).max(120),
  controlUrl: z.string().url(),
  displayBinding: displayBindingSchema.default("shared"),
  pinned: z.boolean().optional(),
  /** Desired state: the app starts enabled networks at launch (including after an update restart). */
  enabled: z.boolean().default(true),
});
export type Membership = z.infer<typeof membershipSchema>;

export const preferencesSchema = z.object({
  /** Open the app at login; it then starts every enabled network. */
  launchAtLogin: z.boolean().default(false),
  fullScreen: z.boolean().default(true),
  presentationMode: z.enum(["automatic", "media", "media-schedule", "web"]).default("automatic"),
});
export type DesktopPreferences = z.infer<typeof preferencesSchema>;

/** `userData/settings.json`. */
export const settingsFileSchema = preferencesSchema.extend({
  networks: z.array(membershipSchema).default([]),
});
export type SettingsFile = z.infer<typeof settingsFileSchema>;

export const joinInputSchema = z.object({
  link: z.string().min(1).max(4096),
  nodeName: z.string().trim().min(1).max(120),
});
export type JoinInput = z.infer<typeof joinInputSchema>;

/** Advanced path for recovery tokens (no join link exists for those). */
export const manualJoinSchema = z.object({
  controlUrl: z.string().url(),
  token: z.string().trim().min(16),
  caSha256: z.string().trim().optional(),
  networkName: z.string().trim().max(120).optional(),
  nodeName: z.string().trim().min(1).max(120),
  recover: z.boolean().default(false),
});
export type ManualJoinInput = z.infer<typeof manualJoinSchema>;

export interface ProvisionedNode {
  version: number;
  nodeId: string;
  nodeName: string;
  controlUrl: string;
  eventEpoch: string;
  publisherKeyId: string;
  /** Absent when control's TLS chain is publicly trusted (system roots). */
  caPath?: string;
  certificatePath: string;
  privateKeyPath: string;
  publisherPublicKeyPath: string;
}

export type NetworkPhase = "stopped" | "starting" | "running" | "error";

export interface NetworkStatus {
  id: string;
  name: string;
  controlUrl: string;
  displayBinding: DisplayBinding;
  /** Display the binding currently resolves to (primary for `shared` or a missing display). */
  displayId?: number;
  pinned: boolean;
  enabled: boolean;
  node?: ProvisionedNode;
  phase: NetworkPhase;
  playerPid?: number;
  lastError?: string;
  /** The sidecar has an output loaded. */
  hasOutput: boolean;
  /** This network owns its display right now. */
  onAir: boolean;
  /** Connected, but its display shows another network. */
  standby: boolean;
  logPath: string;
}

export interface DisplayInfo {
  id: number;
  label: string;
  primary: boolean;
  width: number;
  height: number;
}

export interface UpdateStatus {
  appVersion: string;
  state: "idle" | "checking" | "downloading" | "installing" | "error";
  version?: string;
  percent?: number;
  /** While `installing`: epoch ms when the app restarts into `version`. */
  installAt?: number;
  detail?: string;
}

export interface PendingJoin {
  link: string;
  name: string;
  controlUrl: string;
  caPinned: boolean;
}

export interface DesktopStatus {
  preferences: DesktopPreferences;
  networks: NetworkStatus[];
  displays: DisplayInfo[];
  update: UpdateStatus;
  pendingJoin?: PendingJoin;
  defaultNodeName: string;
}

export interface CuelightNodeDesktopAPI {
  status(): Promise<DesktopStatus>;
  previewJoin(link: string): Promise<DesktopStatus>;
  dismissJoin(): Promise<DesktopStatus>;
  join(input: JoinInput): Promise<DesktopStatus>;
  joinManual(input: ManualJoinInput): Promise<DesktopStatus>;
  rename(id: string, name: string): Promise<DesktopStatus>;
  remove(id: string): Promise<DesktopStatus>;
  setDisplay(id: string, binding: DisplayBinding): Promise<DesktopStatus>;
  setPinned(id: string, pinned: boolean): Promise<DesktopStatus>;
  start(id: string): Promise<DesktopStatus>;
  stop(id: string): Promise<DesktopStatus>;
  updatePreferences(preferences: DesktopPreferences): Promise<DesktopStatus>;
  showOutput(): Promise<void>;
  openLogs(id: string): Promise<void>;
  onStatus(listener: (status: DesktopStatus) => void): () => void;
}

declare global {
  interface Window {
    cuelightNode: CuelightNodeDesktopAPI;
  }
}
