package api

import (
	"encoding/base64"
	"fmt"
	"html"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// Markdown/HTML export (FEAT-038, screens/11-export-dialog.md): a lightweight structured export
// target for embedding into internal wikis and docs. Both formats preserve the report's narrative
// structure (paragraph blocks split on blank lines) and embed the chart as inline SVG — the same
// server-generated chart the image export serves (FEAT-040) — so "includes chart images or
// embeddable chart data" (AC2) holds with no external assets. When the execute output is gone, the
// chart section is replaced by an honest note instead of a fabricated graphic (the same degradation
// the PDF and PPTX apply).

// buildReportMarkdown renders the report as Markdown (FEAT-038): an H1 title, metadata line,
// narrative paragraphs, the chart as a base64 data-URI image (Markdown's portable way to embed an
// image), reconciliation bullets, provenance bullets, and a footer. The data-URI image renders in
// wiki engines that allow data URIs and degrades gracefully elsewhere.
func buildReportMarkdown(report domain.Report, links []domain.ProvenanceLink, exec *domain.ExecuteStepOutput, execAvailable bool) ([]byte, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", report.Title)
	fmt.Fprintf(&b, "_Generated %s · Report %s · Source run %s_\n\n",
		report.CreatedAt.Format("Jan 2, 2006"), report.ID, report.RunID)

	for _, p := range narrativeParagraphs(report.Narrative) {
		fmt.Fprintf(&b, "%s\n\n", p)
	}

	chartTitle := chartDisplayTitle(report.ChartSpec)
	fmt.Fprintf(&b, "## %s\n\n", chartTitle)
	if execAvailable && exec != nil {
		svg, err := buildChartSVG(report.ChartSpec, *exec)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "![%s](data:image/svg+xml;base64,%s)\n\n",
			chartTitle, base64.StdEncoding.EncodeToString(svg))
	} else {
		b.WriteString("> The chart data for this report is no longer available.\n\n")
	}

	b.WriteString("## Reconciliation\n\n")
	fmt.Fprintf(&b, "- Rows read: %d\n", report.ReconciliationSummary.RowsIn)
	fmt.Fprintf(&b, "- Rows after filtering/grouping: %d\n", report.ReconciliationSummary.RowsOut)
	if len(report.ReconciliationSummary.ExclusionWarnings) > 0 {
		for _, w := range report.ReconciliationSummary.ExclusionWarnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	} else {
		b.WriteString("- No significant row exclusions.\n")
	}
	b.WriteString("\n")

	b.WriteString("## Provenance\n\n")
	if len(links) > 0 {
		for _, l := range links {
			fmt.Fprintf(&b, "- **%s** (%s) — source rows: %s\n",
				l.ClaimText, l.StatedValue, strings.Join(l.SourceRowIDs, ", "))
		}
	} else if len(report.ProvenanceLinks) > 0 {
		fmt.Fprintf(&b, "- %d provenance-linked claims (ids: %s)\n",
			len(report.ProvenanceLinks), strings.Join(report.ProvenanceLinks, ", "))
	} else {
		b.WriteString("- No provenance-linked claims in this report.\n")
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "---\n_Exported from Groundwork. Report %s · Source run %s._\n",
		report.ID, report.RunID)

	return []byte(b.String()), nil
}

// buildReportHTML renders the report as a self-contained HTML document (FEAT-038): semantic
// sections for narrative, chart (inline SVG), reconciliation, and provenance, with a minimal style
// block so it reads cleanly when embedded or opened directly.
func buildReportHTML(report domain.Report, links []domain.ProvenanceLink, exec *domain.ExecuteStepOutput, execAvailable bool) ([]byte, error) {
	var b strings.Builder

	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\"/>\n")
	fmt.Fprintf(&b, "<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"/>\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(report.Title))
	b.WriteString(exportHTMLStyles)
	b.WriteString("</head>\n<body>\n<article>\n")

	fmt.Fprintf(&b, "<header><h1>%s</h1><p class=\"meta\">Generated %s · Report %s · Source run %s</p></header>\n",
		html.EscapeString(report.Title), report.CreatedAt.Format("Jan 2, 2006"),
		html.EscapeString(report.ID), html.EscapeString(report.RunID))

	b.WriteString("<section aria-label=\"Narrative\">\n")
	for _, p := range narrativeParagraphs(report.Narrative) {
		fmt.Fprintf(&b, "<p>%s</p>\n", html.EscapeString(p))
	}
	if strings.TrimSpace(report.Narrative) == "" {
		b.WriteString("<p class=\"note\">No narrative was generated for this report.</p>\n")
	}
	b.WriteString("</section>\n")

	chartTitle := chartDisplayTitle(report.ChartSpec)
	fmt.Fprintf(&b, "<section aria-label=\"Chart\"><h2>%s</h2>\n", html.EscapeString(chartTitle))
	if execAvailable && exec != nil {
		svg, err := buildChartSVG(report.ChartSpec, *exec)
		if err != nil {
			return nil, err
		}
		b.Write(svg) // inline SVG — the document stays fully self-contained
	} else {
		b.WriteString("<p class=\"note\">The chart data for this report is no longer available.</p>\n")
	}
	b.WriteString("</section>\n")

	b.WriteString("<section aria-label=\"Reconciliation\"><h2>Reconciliation</h2>\n<ul>\n")
	fmt.Fprintf(&b, "<li>Rows read: %d</li>\n", report.ReconciliationSummary.RowsIn)
	fmt.Fprintf(&b, "<li>Rows after filtering/grouping: %d</li>\n", report.ReconciliationSummary.RowsOut)
	if len(report.ReconciliationSummary.ExclusionWarnings) > 0 {
		for _, w := range report.ReconciliationSummary.ExclusionWarnings {
			fmt.Fprintf(&b, "<li>%s</li>\n", html.EscapeString(w))
		}
	} else {
		b.WriteString("<li>No significant row exclusions.</li>\n")
	}
	b.WriteString("</ul>\n</section>\n")

	b.WriteString("<section aria-label=\"Provenance\"><h2>Provenance</h2>\n<ul>\n")
	if len(links) > 0 {
		for _, l := range links {
			fmt.Fprintf(&b, "<li><strong>%s</strong> (%s) — source rows: %s</li>\n",
				html.EscapeString(l.ClaimText), html.EscapeString(l.StatedValue),
				html.EscapeString(strings.Join(l.SourceRowIDs, ", ")))
		}
	} else if len(report.ProvenanceLinks) > 0 {
		fmt.Fprintf(&b, "<li>%d provenance-linked claims (ids: %s)</li>\n",
			len(report.ProvenanceLinks), html.EscapeString(strings.Join(report.ProvenanceLinks, ", ")))
	} else {
		b.WriteString("<li>No provenance-linked claims in this report.</li>\n")
	}
	b.WriteString("</ul>\n</section>\n")

	fmt.Fprintf(&b, "<footer>Exported from Groundwork. Report %s · Source run %s.</footer>\n",
		html.EscapeString(report.ID), html.EscapeString(report.RunID))

	b.WriteString("</article>\n</body>\n</html>\n")
	return []byte(b.String()), nil
}

// exportHTMLStyles is the self-contained export's minimal stylesheet. It uses the canonical
// LIGHT-mode design token values (web/src/styles/globals.css) because an exported HTML file is a
// file artifact with no theme system — the same precedent the PDF export documents. Only the five
// colors below are mirrored; everything else is semantic HTML the reader's own stylesheet can
// restyle.
const exportHTMLStyles = `<style>
/* Self-contained export artifact: uses the canonical light-mode design tokens from
   web/src/styles/globals.css (--color-text-primary, --color-text-secondary, --color-border,
   --color-surface) — a generated file has no theme system, mirroring the PDF export's palette. */
:root { color-scheme: light; }
body { font-family: Inter, system-ui, -apple-system, "Segoe UI", sans-serif; color: #111418; background: #ffffff; margin: 0; padding: 2rem 1.5rem; line-height: 1.65; }
article { max-width: 46rem; margin: 0 auto; }
h1 { font-size: 1.75rem; line-height: 1.25; margin: 0 0 .25rem; }
h2 { font-size: 1.15rem; margin-top: 1.75rem; }
.meta, footer { color: #5b6270; font-size: .85rem; }
.meta { margin: 0 0 1.5rem; }
section p { margin: .5rem 0; }
.note { color: #5b6270; font-style: italic; }
ul { padding-left: 1.25rem; }
li { margin: .25rem 0; }
svg { max-width: 100%; height: auto; border: 1px solid #dfe1e6; border-radius: 6px; }
footer { border-top: 1px solid #dfe1e6; margin-top: 2rem; padding-top: .75rem; }
</style>`

// narrativeParagraphs splits the narrative into paragraph blocks on blank lines, mirroring how the
// report view renders the narrative as blocks. A single-line narrative yields one paragraph.
func narrativeParagraphs(narrative string) []string {
	var out []string
	for _, block := range strings.Split(narrative, "\n\n") {
		block = strings.TrimSpace(block)
		if block != "" {
			out = append(out, block)
		}
	}
	if len(out) == 0 && strings.TrimSpace(narrative) != "" {
		out = append(out, strings.TrimSpace(narrative))
	}
	return out
}
