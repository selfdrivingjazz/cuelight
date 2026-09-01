import type { CredentialStorage, NodeCredential } from "./types";

const STORAGE_KEY = "cable.website-node.credential";

/** Stores only the scoped credential; assignments and media are never persisted. */
export class LocalCredentialStorage implements CredentialStorage {
  constructor(private readonly storage: Storage = window.localStorage) {}

  load(): NodeCredential | null {
    const raw = this.storage.getItem(STORAGE_KEY);
    if (!raw) return null;
    try {
      const value = JSON.parse(raw) as Partial<NodeCredential>;
      if (typeof value.nodeId !== "string" || typeof value.token !== "string" || typeof value.expiresAt !== "string") {
        this.clear();
        return null;
      }
      return { nodeId: value.nodeId, token: value.token, expiresAt: value.expiresAt };
    } catch {
      this.clear();
      return null;
    }
  }

  save(credential: NodeCredential): void {
    this.storage.setItem(STORAGE_KEY, JSON.stringify(credential));
  }

  clear(): void {
    this.storage.removeItem(STORAGE_KEY);
  }
}

export class MemoryCredentialStorage implements CredentialStorage {
  private value: NodeCredential | null = null;
  load(): NodeCredential | null { return this.value ? { ...this.value } : null; }
  save(credential: NodeCredential): void { this.value = { ...credential }; }
  clear(): void { this.value = null; }
}

export { STORAGE_KEY };
