import type { Metadata } from "next";
import { NotificationsScreen } from "@/components/notifications/NotificationsScreen";

export const metadata: Metadata = { title: "Notifications" };

/** Route: `/notifications` — see docs/Groundwork/screens/16-notifications.md (FEAT-026) */
export default function Page() {
  return <NotificationsScreen />;
}
