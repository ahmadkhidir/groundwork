/**
 * Default route-level loading state.
 *
 * Carries a text label, not a bare spinner: system state is always stated honestly, and a
 * screen-reader user needs to know the page is working rather than finished and empty.
 */
export default function Loading() {
  return (
    <main
      id="main-content"
      className="flex flex-1 items-center justify-center p-xl"
      aria-busy="true"
    >
      <p role="status" className="text-body text-text-secondary">
        Loading…
      </p>
    </main>
  );
}
