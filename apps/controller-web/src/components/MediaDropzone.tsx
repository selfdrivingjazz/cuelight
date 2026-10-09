import * as React from "react";
import { useMutation } from "@tanstack/react-query";
import { type components } from "@cable/api-client";
import { Button, ErrorState } from "@cable/controller-ui";
import { api, readApiResult } from "../api";
import { uploadSelectedFile } from "../upload";

const acceptedMediaTypes: Record<string, true> = { "image/jpeg": true, "image/png": true, "image/webp": true, "video/mp4": true };
const mediaAccept = "image/jpeg,image/png,image/webp,video/mp4";

function fileSize(size: number) {
  if (size < 1_000) return `${size.toLocaleString()} B`;
  if (size < 1_000_000) return `${(size / 1_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} KB`;
  return `${(size / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} MB`;
}

type Media = components["schemas"]["Media"];

async function uploadMedia(file: File): Promise<Media> {
  const asset = await uploadSelectedFile(file);
  return readApiResult(await api.POST("/media", {
    params: { header: { "Idempotency-Key": crypto.randomUUID() } },
    body: {
      kind: "file",
      appAccess: false,
      assetId: asset.id,
      title: asset.name,
      tags: [],
      playback: { fit: "fit", loop: true, muted: true, volume: 0, orientation: 0 },
    },
  }));
}

export function MediaDropzone({ onUploaded }: { onUploaded: (media: Media) => void }) {
  const [file, setFile] = React.useState<File>();
  const [validationError, setValidationError] = React.useState<string>();
  const [dragActive, setDragActive] = React.useState(false);
  const [inputKey, setInputKey] = React.useState(0);
  const dragDepth = React.useRef(0);
  const typesId = React.useId();
  const mutation = useMutation({
    mutationFn: uploadMedia,
    onSuccess: (media) => onUploaded(media),
  });

  const selectFile = (nextFile?: File) => {
    mutation.reset();
    setValidationError(undefined);
    if (!nextFile) {
      setFile(undefined);
      return;
    }
    if (!acceptedMediaTypes[nextFile.type]) {
      setFile(undefined);
      setValidationError("Choose a JPEG, PNG, WebP, or MP4 file.");
      return;
    }
    setFile(nextFile);
  };

  const reset = () => {
    mutation.reset();
    setFile(undefined);
    setValidationError(undefined);
    setInputKey((value) => value + 1);
  };

  const dropzoneTitle = dragActive
    ? "Drop to add this file"
    : mutation.isPending
      ? `Uploading ${file?.name ?? "file"}`
      : mutation.isSuccess
        ? "Upload complete"
        : file
          ? "Ready to upload"
          : "Drop an image or video here";

  return <form className="space-y-4" onSubmit={(event) => {
    event.preventDefault();
    if (file) mutation.mutate(file);
  }}>
    <div
      role="group"
      aria-label="Media upload"
      className={`relative flex min-h-36 flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-5 text-center transition-colors focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-2 sm:p-6 ${dragActive ? "border-primary bg-primary/10" : "border-border bg-muted/30"} ${mutation.isPending ? "cursor-wait opacity-75" : ""}`}
      onDragEnter={(event) => {
        event.preventDefault();
        if (mutation.isPending) return;
        dragDepth.current += 1;
        setDragActive(true);
      }}
      onDragOver={(event) => {
        event.preventDefault();
        if (!mutation.isPending) event.dataTransfer.dropEffect = "copy";
      }}
      onDragLeave={(event) => {
        event.preventDefault();
        dragDepth.current = Math.max(0, dragDepth.current - 1);
        if (dragDepth.current === 0) setDragActive(false);
      }}
      onDrop={(event) => {
        event.preventDefault();
        dragDepth.current = 0;
        setDragActive(false);
        if (!mutation.isPending) selectFile(event.dataTransfer.files[0]);
      }}
    >
      <input
        key={inputKey}
        type="file"
        accept={mediaAccept}
        aria-label="Choose an image or video"
        aria-describedby={typesId}
        disabled={mutation.isPending}
        className="absolute inset-0 z-10 h-full w-full cursor-pointer opacity-0 disabled:cursor-wait"
        onChange={(event) => selectFile(event.currentTarget.files?.[0])}
      />
      <p className="text-sm font-medium">{dropzoneTitle}</p>
      {file && <p className="max-w-full break-all text-sm text-muted-foreground">{file.name} · {fileSize(file.size)}</p>}
      {!file && <p className="text-sm font-medium text-primary underline underline-offset-4">Choose a file</p>}
      <p id={typesId} className="text-xs text-muted-foreground">JPEG, PNG, WebP, or MP4{file && !mutation.isPending ? " · Choose another file to replace it" : ""}</p>
    </div>

    {mutation.isPending && <div className="space-y-2" aria-live="polite">
      <progress className="h-2 w-full accent-primary" aria-label={`Uploading ${file?.name ?? "file"}`} aria-valuetext="Upload in progress" />
      <p className="text-sm text-muted-foreground">Uploading file…</p>
    </div>}
    {validationError && <ErrorState title="File not supported" description={validationError} />}
    {mutation.isError && <ErrorState title="Upload failed" description={mutation.error.message} onRetry={() => { if (file) mutation.mutate(file); }} />}
    {mutation.data && <div role="status" className="space-y-2 rounded-lg border border-status-success/40 bg-status-success/5 p-4">
      <p className="font-medium text-status-success">{mutation.data.title} is ready in your library.</p>
      <a className="inline-block text-sm font-medium text-primary hover:underline" href={`/library/media/${encodeURIComponent(mutation.data.id)}`}>View {mutation.data.title}</a>
    </div>}
    <div className="flex flex-wrap gap-2">
      {file && !mutation.isSuccess && !mutation.isError && <Button type="submit" disabled={mutation.isPending}>{mutation.isPending ? "Uploading…" : "Upload media"}</Button>}
      {(file || validationError) && !mutation.isPending && <Button type="button" variant="ghost" onClick={reset}>{mutation.isSuccess ? "Upload another file" : "Clear file"}</Button>}
    </div>
  </form>;
}
