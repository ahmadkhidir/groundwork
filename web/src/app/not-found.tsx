import Link from "next/link";
import { buttonClassNames } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { routes } from "@/lib/routes";

/** 404 state. Offers a route back rather than leaving the user stranded. */
export default function NotFound() {
  return (
    <main id="main-content" className="mx-auto w-full max-w-xl p-xl">
      <Card>
        <h1 className="text-h2 text-text-primary">Page not found</h1>
        <p className="mt-sm text-body text-text-secondary">
          That page does not exist, or you do not have access to it.
        </p>
        <div className="mt-lg">
          <Link href={routes.workspaces()} className={buttonClassNames("primary")}>
            Go to workspaces
          </Link>
        </div>
      </Card>
    </main>
  );
}
