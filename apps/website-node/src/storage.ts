import type { BrowserCredential } from "./types";

export const STORAGE_KEY = "cuelight.browser-node.credential";

export interface CredentialStorage {
  load(): BrowserCredential | null;
  save(credential: BrowserCredential): void;
  clear(): void;
}

/** Persists only the scoped browser credential; assignments and media are never stored. */
export class LocalCredentialStorage implements CredentialStorage {
  constructor(private readonly storage: Storage = window.localStorage) {}

  load(): BrowserCredential | null {
    const raw = this.storage.getItem(STORAGE_KEY);
    if (!raw) return null;
    try {
      const value = JSON.parse(raw) as Partial<BrowserCredential>;
      if (typeof value.nodeId === "string" && typeof value.token === "string" && typeof value.expiresAt === "string"
        && typeof value.renewAfter === "string" && typeof value.eventEpoch === "string") {
        return { nodeId: value.nodeId, token: value.token, expiresAt: value.expiresAt, renewAfter: value.renewAfter, eventEpoch: value.eventEpoch };
      }
    } catch {
      // Corrupt values are discarded below.
    }
    this.clear();
    return null;
  }

  save(credential: BrowserCredential): void {
    this.storage.setItem(STORAGE_KEY, JSON.stringify(credential));
  }

  clear(): void {
    this.storage.removeItem(STORAGE_KEY);
  }
}

export class MemoryCredentialStorage implements CredentialStorage {
  private value: BrowserCredential | null = null;
  load(): BrowserCredential | null { return this.value ? { ...this.value } : null; }
  save(credential: BrowserCredential): void { this.value = { ...credential }; }
  clear(): void { this.value = null; }
}
