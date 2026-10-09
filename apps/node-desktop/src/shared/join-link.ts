/** A parsed `cuelight://join?v=1&url=&token=[&ca=sha256:]&name=` link. */
export interface JoinLink {
  /** HTTPS origin (optionally with a path prefix) of the control plane. */
  controlUrl: string;
  /** One-time enrollment secret. */
  token: string;
  /** Lowercase hex SHA-256 of the self-signed root DER, when the chain is not publicly trusted. */
  caSha256?: string;
  /** Human network name. */
  name: string;
}

export class JoinLinkError extends Error {}

/** Parses and validates a join link; throws JoinLinkError with a user-facing reason. */
export function parseJoinLink(raw: string): JoinLink {
  let link: URL;
  try { link = new URL(raw.trim()); }
  catch { throw new JoinLinkError("not a link"); }
  // `cuelight://join?…` parses with host "join"; some launchers hand over `cuelight:join?…`.
  const target = link.host || link.pathname.replace(/^\/+/, "");
  if (link.protocol !== "cuelight:" || target.replace(/\/+$/, "") !== "join") throw new JoinLinkError("not a cuelight://join link");
  const query = link.searchParams;
  if (query.get("v") !== "1") throw new JoinLinkError(`unsupported join link version ${JSON.stringify(query.get("v") ?? "")}; update Cuelight`);
  const controlUrl = parseControlURL(query.get("url"));
  const token = query.get("token")?.trim() ?? "";
  if (token.length < 16 || token.length > 512 || /\s/.test(token)) throw new JoinLinkError("join link has no valid enrollment token");
  const ca = query.get("ca");
  let caSha256: string | undefined;
  if (ca !== null) {
    const match = /^sha256:([0-9a-f]{64})$/i.exec(ca.trim());
    if (!match?.[1]) throw new JoinLinkError("join link CA pin must be sha256:<64 hex digits>");
    caSha256 = match[1].toLowerCase();
  }
  const name = (query.get("name") ?? "").trim().slice(0, 120) || new URL(controlUrl).hostname;
  return { controlUrl, token, caSha256, name };
}

/** Normalises an operator-entered CA fingerprint (`AB:CD:…` or plain hex) to lowercase hex. */
export function normalizeSHA256(value: string): string | undefined {
  const hex = value.trim().replace(/^sha256:/i, "").replaceAll(":", "").toLowerCase();
  return /^[0-9a-f]{64}$/.test(hex) ? hex : undefined;
}

function parseControlURL(value: string | null): string {
  let parsed: URL;
  try { parsed = new URL(value ?? ""); }
  catch { throw new JoinLinkError("join link has no control URL"); }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new JoinLinkError("join link control URL must be HTTPS without credentials, query, or fragment");
  }
  return parsed.toString().replace(/\/+$/, "");
}
