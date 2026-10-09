import type { components } from "@cuelight/api-client";
import { api, readApiResult } from "./api";

/** Uploads a file through the resumable upload API and returns the completed asset. */
export async function uploadSelectedFile(file: File): Promise<components["schemas"]["Asset"]> {
  const created = readApiResult(await api.POST("/uploads", { params: { header: { "Idempotency-Key": crypto.randomUUID() } }, body: { name: file.name, length: file.size, mediaType: file.type || "application/octet-stream" } }));
  let offset = created.offset;
  const chunkSize = 1024 * 1024;
  while (offset < file.size) {
    const chunk = file.slice(offset, Math.min(offset + chunkSize, file.size));
    const response = await api.PATCH("/uploads/{uploadId}", { params: { path: { uploadId: created.id }, header: { "Upload-Offset": offset } }, headers: { "Content-Type": "application/offset+octet-stream" }, body: chunk as unknown as string, bodySerializer: () => chunk });
    if (response.error) throw new Error("The controller rejected an upload chunk.");
    const accepted = response.response?.headers.get("Upload-Offset");
    offset = accepted ? Number(accepted) : offset + chunk.size;
  }
  return readApiResult(await api.POST("/uploads/{uploadId}/complete", { params: { path: { uploadId: created.id }, header: { "Idempotency-Key": crypto.randomUUID() } } }));
}
