import type { Metadata } from "next";
import { UploadScreen } from "@/components/upload/UploadScreen";

export const metadata: Metadata = { title: "Upload data" };

/**
 * Route: `/workspaces/[workspaceId]/upload` — see docs/Groundwork/screens/05-file-upload.md.
 *
 * FEAT-003 (this wave) implements the upload itself. FEAT-004 (session-scoped engine provisioning
 * and ingest, Wave 3) is what turns an uploaded file into a queryable one — this screen hands off to
 * Ingest Progress once every selected file finishes uploading, without waiting on that pipeline.
 */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/upload">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  return <UploadScreen workspaceId={workspaceId} />;
}
