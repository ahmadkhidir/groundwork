package api

import (
	"bytes"
	"fmt"
	"math"
	"strings"

	"github.com/go-pdf/fpdf"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// Report PDF export (FEAT-035, screens/11-export-dialog.md): a server-generated, print-oriented PDF
// containing the report's narrative text and a chart drawn from the same execute facts the in-app
// ChartRenderer draws from (report.chart_spec + the run's execute step output), so the exported
// chart is consistent with the in-app rendering (FEAT-035 AC2) and requires no live engine
// (FEAT-035 AC3 — everything comes from the Report Store and persisted steps).
//
// PDF mechanism choice (recorded for the orchestrator): the document is generated server-side with
// github.com/go-pdf/fpdf (v0.9.0, pure Go), using its built-in core fonts — no TTF asset, no headless
// browser, no client print dialog. The chart is drawn with vector primitives (rects, polylines, pie
// polygons, circles) that mirror ChartRenderer's geometry: same category/series extraction, same
// scaleMax, same color-per-series assignment, using the same branding chart token values (mirrored
// below, since a generated PDF is a file artifact with no CSS theme system). The one deliberate
// divergence is null cell rendering: the in-app table shows "—" while the PDF chart label sanitizer
// maps that to "-" (WinAnsi) — visually equivalent, and documented in sanitizePdfText.

// PDF page constants. A4 portrait, 20mm margins; all drawing units are millimetres.
const (
	pdfMargin      = 20.0
	pdfContentW    = 170.0 // 210 - 2*20
	pdfPlotHeight  = 70.0
	pdfPageBottom  = 297.0 - pdfMargin
	pdfMaxTableRow = 14 // rows drawn before the table notes the remainder
)

// PDF color constants mirroring branding.md's light-mode design tokens (globals.css custom
// properties). A generated PDF has no theme system — the client renderer's dark-mode switch does not
// apply to a printed document — so the canonical light values are the print palette, defined once
// here rather than inlined at call sites.
var (
	pdfTextPrimary   = [3]int{0x11, 0x14, 0x18} // --color-text-primary
	pdfTextSecondary = [3]int{0x5b, 0x62, 0x70} // --color-text-secondary
	pdfBorder        = [3]int{0xdf, 0xe1, 0xe6} // --color-border
	pdfSurface       = [3]int{0xf4, 0xf5, 0xf7} // --color-surface-secondary
)

// pdfChartPalette mirrors branding.md's chart-cat-1..8 light values (globals.css
// --color-chart-cat-1..8) in fixed order, matching ChartRenderer's CAT_FILLS/CAT_STROKES
// assignment: the same metric renders in the same color in the PDF as in the app.
var pdfChartPalette = [][3]int{
	{0x1d, 0x4e, 0xd8}, // chart-cat-1 (primary)
	{0xb4, 0x53, 0x09}, // chart-cat-2
	{0x0f, 0x76, 0x6e}, // chart-cat-3 (provenance)
	{0x7c, 0x3a, 0xed}, // chart-cat-4
	{0xbe, 0x18, 0x5d}, // chart-cat-5
	{0x4d, 0x7c, 0x0f}, // chart-cat-6
	{0x03, 0x69, 0xa1}, // chart-cat-7
	{0x78, 0x71, 0x6c}, // chart-cat-8
}

// buildReportPDF renders the report as a print-oriented A4 PDF: title + generated date, narrative
// (the primary reading surface), the chart (or an honest "no longer available" note when the
// execute output is gone), the reconciliation notice when exclusions were disclosed, and a footer
// identifying the report and source run.
func buildReportPDF(report domain.Report, exec *domain.ExecuteStepOutput, execAvailable bool) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(pdfMargin, pdfMargin, pdfMargin)
	pdf.SetAutoPageBreak(true, pdfMargin)
	pdf.AddPage()

	// Title + generated date.
	pdf.SetTextColor(pdfTextPrimary[0], pdfTextPrimary[1], pdfTextPrimary[2])
	pdf.SetFont("Helvetica", "B", 18)
	pdf.MultiCell(pdfContentW, 9, sanitizePdfText(report.Title), "", "L", false)
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
	pdf.MultiCell(pdfContentW, 4.5, "Generated "+report.CreatedAt.Format("Jan 2, 2006"), "", "L", false)
	pdf.Ln(4)

	// Narrative — the primary reading surface, wrapped across pages.
	pdf.SetTextColor(pdfTextPrimary[0], pdfTextPrimary[1], pdfTextPrimary[2])
	pdf.SetFont("Helvetica", "", 11)
	pdf.MultiCell(pdfContentW, 5.5, sanitizePdfText(report.Narrative), "", "L", false)

	pdf.Ln(5)
	if execAvailable && exec != nil {
		title := report.ChartSpec.Title
		if title == "" {
			title = "Result"
		}
		pdf.SetFont("Helvetica", "B", 13)
		pdf.MultiCell(pdfContentW, 7, sanitizePdfText(title), "", "L", false)
		pdf.Ln(2)
		drawChartPDF(pdf, report.ChartSpec, *exec)
	} else {
		// The honest degradation when the underlying result data is gone (mirrors the provenance
		// panel's "source unavailable" state): narrative-only, with the gap stated rather than a
		// fabricated chart.
		pdf.SetFont("Helvetica", "I", 10)
		pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
		pdf.MultiCell(pdfContentW, 5, "The chart data for this report is no longer available.", "", "L", false)
		pdf.SetTextColor(pdfTextPrimary[0], pdfTextPrimary[1], pdfTextPrimary[2])
	}

	// Reconciliation notice: unprompted disclosure of significant row exclusions, near the end like
	// a footnote — the same honesty the in-app notice provides.
	if len(report.ReconciliationSummary.ExclusionWarnings) > 0 {
		pdf.Ln(5)
		pdf.SetFont("Helvetica", "B", 11)
		pdf.MultiCell(pdfContentW, 6, "Reconciliation note", "", "L", false)
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
		for _, warning := range report.ReconciliationSummary.ExclusionWarnings {
			pdf.MultiCell(pdfContentW, 5, "- "+sanitizePdfText(warning), "", "L", false)
		}
		pdf.SetTextColor(pdfTextPrimary[0], pdfTextPrimary[1], pdfTextPrimary[2])
	}

	// Footer: report + source run identity, so the printout is traceable back to its provenance.
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
	pdf.MultiCell(pdfContentW, 4, fmt.Sprintf("Report %s · source run %s", report.ID, report.RunID), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("rendering pdf: %w", err)
	}
	return buf.Bytes(), nil
}

// drawChartPDF dispatches to the per-type drawer. Chart types the PDF drawer cannot draw (table, or
// a future spec'd type) fall back to the data table — mirroring ChartRenderer's in-app fallback.
func drawChartPDF(pdf *fpdf.Fpdf, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
	points := computeChartPoints(spec, exec)
	switch spec.ChartType {
	case domain.ChartBar:
		drawBarChartPDF(pdf, points)
	case domain.ChartLine:
		drawLineChartPDF(pdf, points)
	case domain.ChartPie:
		drawPieChartPDF(pdf, points)
	case domain.ChartScatter:
		drawScatterChartPDF(pdf, points)
	default:
		drawTablePDF(pdf, exec)
	}
}

// pdfChartPoint is one flattened chart datum, mirroring ChartRenderer's Point shape: the display
// label, the series name (empty for single-series), and the numeric value (hasValue false when the
// cell was not numeric).
type pdfChartPoint struct {
	label    string
	series   string
	value    float64
	hasValue bool
}

// computeChartPoints flattens the executed result set into chart points using the spec's column
// encodings, mirroring ChartRenderer's toPoints + toNumber (chart-data.ts): the label is the x cell
// rendered as display text, the value is the y cell coerced to a number (numeric strings accepted,
// booleans rejected), and the series is the series cell when one is mapped.
func computeChartPoints(spec domain.ChartSpec, exec domain.ExecuteStepOutput) []pdfChartPoint {
	xIdx := indexOfColumn(exec.Columns, spec.XAxisColumn)
	yIdx := indexOfColumn(exec.Columns, spec.YAxisColumn)
	sIdx := indexOfColumn(exec.Columns, spec.SeriesColumn)

	out := make([]pdfChartPoint, 0, len(exec.Rows))
	for _, row := range exec.Rows {
		p := pdfChartPoint{}
		if xIdx >= 0 && xIdx < len(row) {
			p.label = pdfDisplayValue(row[xIdx])
		}
		if sIdx >= 0 && sIdx < len(row) {
			p.series = pdfDisplayValue(row[sIdx])
		}
		if yIdx >= 0 && yIdx < len(row) {
			if v, ok := pdfToNumber(row[yIdx]); ok {
				p.value = v
				p.hasValue = true
			}
		}
		out = append(out, p)
	}
	return out
}

// indexOfColumn returns the index of column in columns, or -1 (mirroring resolveColumnIndex: an
// empty name is never found).
func indexOfColumn(columns []string, column string) int {
	if column == "" {
		return -1
	}
	for i, c := range columns {
		if c == column {
			return i
		}
	}
	return -1
}

// pdfDisplayValue renders a cell as display text, mirroring chart-data.ts's displayValue: null
// renders as the visible "—" em dash (the sanitizer maps it to a WinAnsi-safe "-" for PDF text).
func pdfDisplayValue(v any) string {
	if v == nil {
		return "—"
	}
	return formatCell(v)
}

// pdfToNumber coerces a cell to a number, mirroring chart-data.ts's toNumber: numbers pass through,
// numeric strings parse, booleans and everything else are not numeric.
func pdfToNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, !math.IsNaN(t) && !math.IsInf(t, 0)
	case jsonNumberLike:
		f, err := t.Float64()
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case string:
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			return 0, false
		}
		var f float64
		if _, err := fmt.Sscanf(trimmed, "%g", &f); err != nil {
			return 0, false
		}
		return f, !math.IsNaN(f) && !math.IsInf(f, 0)
	default:
		return 0, false
	}
}

// jsonNumberLike is the numeric JSON token encoding/json produces for numbers inside ExecuteStepOutput
// rows; json.Number satisfies it. Defined as an interface here so the coercion works whether the
// step output was decoded with UseNumber or not.
type jsonNumberLike interface {
	Float64() (float64, error)
}

// uniqueSeries returns the distinct series names in first-appearance order, mirroring
// ChartRenderer's uniqueValues. A single empty series (no series_column mapped) yields [""].
func uniqueSeries(points []pdfChartPoint) []string {
	var out []string
	for _, p := range points {
		if !containsString(out, p.series) {
			out = append(out, p.series)
		}
	}
	return out
}

// uniqueLabels returns the distinct category labels in first-appearance order.
func uniqueLabels(points []pdfChartPoint) []string {
	var out []string
	for _, p := range points {
		if !containsString(out, p.label) {
			out = append(out, p.label)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// scaleMaxFor mirrors ChartRenderer's scaleMax: the maximum value across points, floored at 1 so a
// flat or empty chart still has a defined scale.
func scaleMaxFor(points []pdfChartPoint) float64 {
	max := 1.0
	for _, p := range points {
		if p.hasValue && p.value > max {
			max = p.value
		}
	}
	return max
}

// pdfFormatTick mirrors ChartRenderer's formatTick: large numbers compact ("1.5k"), integers plain,
// others with one decimal.
func pdfFormatTick(value float64) string {
	if math.Abs(value) >= 1000 {
		return fmt.Sprintf("%dk", int64(math.Round(value/1000)))
	}
	if value == math.Trunc(value) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%.1f", value)
}

// pdfGridlines draws four horizontal gridlines with y-axis labels, mirroring ChartRenderer's
// AxisGrid.
func pdfGridlines(pdf *fpdf.Fpdf, left, top, width, height, scaleMax float64) {
	pdf.SetDrawColor(pdfBorder[0], pdfBorder[1], pdfBorder[2])
	pdf.SetLineWidth(0.2)
	pdf.SetFont("Helvetica", "", 7)
	pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
	steps := 4.0
	for i := 0.0; i <= steps; i++ {
		value := scaleMax * i / steps
		y := top + height - (value/scaleMax)*height
		pdf.Line(left, y, left+width, y)
		pdf.SetXY(left-10, y-1.5)
		pdf.CellFormat(8, 3, pdfFormatTick(value), "", 0, "R", false, 0, "")
	}
}

func drawBarChartPDF(pdf *fpdf.Fpdf, points []pdfChartPoint) {
	series := uniqueSeries(points)
	categories := uniqueLabels(points)
	scaleMax := scaleMaxFor(points)

	left := pdfMargin + 8
	width := pdfContentW - 8
	height := pdfPlotHeight
	ensurePdfSpace(pdf, height+14)
	top := pdf.GetY() + 2

	pdfGridlines(pdf, left, top, width, height, scaleMax)

	groupWidth := width / math.Max(1, float64(len(categories)))
	barWidth := groupWidth/math.Max(1, float64(len(series))) - 1.0

	for ci, category := range categories {
		x := left + float64(ci)*groupWidth
		for si, s := range series {
			val := 0.0
			for _, p := range points {
				if p.label == category && p.series == s && p.hasValue {
					val = p.value
					break
				}
			}
			h := (val / scaleMax) * height
			palette := pdfChartPalette[si%len(pdfChartPalette)]
			pdf.SetFillColor(palette[0], palette[1], palette[2])
			pdf.Rect(x+float64(si)*barWidth+0.5, top+height-h, math.Max(barWidth, 0.5), h, "F")
		}
		if len(categories) <= 10 {
			pdf.SetFont("Helvetica", "", 7)
			pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
			pdf.SetXY(x, top+height+1)
			pdf.CellFormat(groupWidth, 3, sanitizePdfText(category), "", 0, "C", false, 0, "")
		}
	}
	pdf.SetY(top + height + 6)
}

func drawLineChartPDF(pdf *fpdf.Fpdf, points []pdfChartPoint) {
	series := uniqueSeries(points)
	scaleMax := scaleMaxFor(points)

	left := pdfMargin + 8
	width := pdfContentW - 8
	height := pdfPlotHeight
	ensurePdfSpace(pdf, height+8)
	top := pdf.GetY() + 2

	maxX := 0.0
	useNumericX := false
	for _, p := range points {
		if v, ok := pdfToNumber(p.label); ok && v > maxX {
			maxX = v
		}
	}
	useNumericX = maxX > 0

	pdfGridlines(pdf, left, top, width, height, scaleMax)
	pdf.SetLineWidth(0.8)

	for si, s := range series {
		var pts []pdfChartPoint
		for _, p := range points {
			if p.series == s {
				pts = append(pts, p)
			}
		}
		if len(pts) == 0 {
			continue
		}
		palette := pdfChartPalette[si%len(pdfChartPalette)]
		pdf.SetDrawColor(palette[0], palette[1], palette[2])
		prevX, prevY := 0.0, 0.0
		first := true
		for i, p := range pts {
			x := 0.0
			if useNumericX {
				if v, ok := pdfToNumber(p.label); ok {
					x = left + (v/maxX)*width
				} else {
					continue
				}
			} else {
				x = left + (float64(i)/math.Max(1, float64(len(pts)-1)))*width
			}
			y := top + height - ((p.valueOrZero())/scaleMax)*height
			if first {
				prevX, prevY = x, y
				first = false
				continue
			}
			pdf.Line(prevX, prevY, x, y)
			prevX, prevY = x, y
		}
	}
	pdf.SetY(top + height + 6)
}

func (p pdfChartPoint) valueOrZero() float64 {
	if p.hasValue {
		return p.value
	}
	return 0
}

func drawPieChartPDF(pdf *fpdf.Fpdf, points []pdfChartPoint) {
	totals := map[string]float64{}
	var labels []string
	for _, p := range points {
		if !p.hasValue {
			continue
		}
		if _, seen := totals[p.label]; !seen {
			labels = append(labels, p.label)
		}
		totals[p.label] += p.value
	}
	sum := 0.0
	for _, v := range totals {
		sum += v
	}

	left := pdfMargin + 8
	width := pdfContentW - 8
	height := pdfPlotHeight
	ensurePdfSpace(pdf, height+10+float64(len(labels))*4)
	top := pdf.GetY() + 2

	if sum == 0 || len(labels) == 0 {
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
		pdf.MultiCell(width, 5, "No numeric values to chart.", "", "L", false)
		pdf.SetY(pdf.GetY() + 4)
		return
	}

	cx := left + width/2
	cy := top + height/2
	r := math.Min(width, height)/2 - 2

	cursor := -math.Pi / 2
	for i, label := range labels {
		sweep := (totals[label] / sum) * math.Pi * 2
		// A wedge as a filled polygon: center + arc points, ~24 segments per half turn.
		poly := make([]fpdf.PointType, 0, 40)
		poly = append(poly, fpdf.PointType{X: cx, Y: cy})
		segments := int(sweep/(math.Pi/24)) + 2
		for s := 0; s <= segments; s++ {
			angle := cursor + sweep*float64(s)/float64(segments)
			poly = append(poly, fpdf.PointType{
				X: cx + r*math.Cos(angle),
				Y: cy + r*math.Sin(angle),
			})
		}
		palette := pdfChartPalette[i%len(pdfChartPalette)]
		pdf.SetFillColor(palette[0], palette[1], palette[2])
		pdf.Polygon(poly, "F")
		cursor += sweep
	}

	// Legend below the pie: swatch + label + share, one line per slice.
	pdf.SetY(top + height + 4)
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
	for i, label := range labels {
		palette := pdfChartPalette[i%len(pdfChartPalette)]
		pdf.SetFillColor(palette[0], palette[1], palette[2])
		pdf.Rect(pdfMargin+8, pdf.GetY()+1.2, 3, 3, "F")
		share := totals[label] / sum * 100
		pdf.SetXY(pdfMargin+13, pdf.GetY())
		pdf.CellFormat(width-5, 4, fmt.Sprintf("%s — %.0f%%", sanitizePdfText(label), share), "", 0, "L", false, 0, "")
		pdf.Ln(4)
	}
	pdf.SetY(pdf.GetY() + 2)
}

func drawScatterChartPDF(pdf *fpdf.Fpdf, points []pdfChartPoint) {
	series := uniqueSeries(points)
	scaleMax := scaleMaxFor(points)

	left := pdfMargin + 8
	width := pdfContentW - 8
	height := pdfPlotHeight
	ensurePdfSpace(pdf, height+8)
	top := pdf.GetY() + 2

	maxX := 0.0
	for _, p := range points {
		if v, ok := pdfToNumber(p.label); ok && v > maxX {
			maxX = v
		}
	}
	if maxX == 0 {
		maxX = 1
	}

	pdfGridlines(pdf, left, top, width, height, scaleMax)
	for _, p := range points {
		if !p.hasValue {
			continue
		}
		xNum, ok := pdfToNumber(p.label)
		if !ok {
			continue
		}
		x := left + (xNum/maxX)*width
		y := top + height - (p.value/scaleMax)*height
		si := indexOfString(series, p.series)
		if si < 0 {
			si = 0
		}
		palette := pdfChartPalette[si%len(pdfChartPalette)]
		pdf.SetFillColor(palette[0], palette[1], palette[2])
		pdf.Circle(x, y, 1.4, "F")
	}
	pdf.SetY(top + height + 6)
}

func indexOfString(list []string, s string) int {
	for i, item := range list {
		if item == s {
			return i
		}
	}
	return -1
}

// drawTablePDF renders the executed result set as a small data table — the PDF equivalent of the
// AccessibleDataTable fallback the in-app renderer uses for table-type or unsupported chart specs.
// Capped at pdfMaxTableRow rows; the remainder is named rather than silently dropped.
func drawTablePDF(pdf *fpdf.Fpdf, exec domain.ExecuteStepOutput) {
	cols := exec.Columns
	if len(cols) == 0 {
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
		pdf.MultiCell(pdfContentW, 5, "No result data to chart.", "", "L", false)
		return
	}

	top := pdf.GetY() + 2
	colWidth := (pdfContentW - 8) / float64(len(cols))
	rowHeight := 5.0
	headerHeight := 6.0
	rows := len(exec.Rows)
	if rows > pdfMaxTableRow {
		rows = pdfMaxTableRow
	}
	tableHeight := headerHeight + float64(rows)*rowHeight
	ensurePdfSpace(pdf, tableHeight+8)
	top = pdf.GetY() + 2

	// Header row.
	pdf.SetFont("Helvetica", "B", 7)
	pdf.SetTextColor(pdfTextPrimary[0], pdfTextPrimary[1], pdfTextPrimary[2])
	pdf.SetFillColor(pdfSurface[0], pdfSurface[1], pdfSurface[2])
	for ci, col := range cols {
		pdf.SetXY(pdfMargin+8+float64(ci)*colWidth, top)
		pdf.CellFormat(colWidth-1, headerHeight, sanitizePdfText(truncatePdfCell(col, colWidth)), "1", 0, "L", true, 0, "")
	}

	// Data rows.
	pdf.SetFont("Helvetica", "", 7)
	pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
	for ri := 0; ri < rows; ri++ {
		row := exec.Rows[ri]
		for ci := 0; ci < len(cols); ci++ {
			cell := ""
			if ci < len(row) {
				cell = pdfDisplayValue(row[ci])
			}
			pdf.SetXY(pdfMargin+8+float64(ci)*colWidth, top+headerHeight+float64(ri)*rowHeight)
			pdf.CellFormat(colWidth-1, rowHeight, sanitizePdfText(truncatePdfCell(cell, colWidth)), "1", 0, "L", false, 0, "")
		}
	}

	pdf.SetY(top + tableHeight + 2)
	if len(exec.Rows) > pdfMaxTableRow {
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(pdfTextSecondary[0], pdfTextSecondary[1], pdfTextSecondary[2])
		pdf.MultiCell(pdfContentW, 4, fmt.Sprintf("… %d more rows", len(exec.Rows)-pdfMaxTableRow), "", "L", false)
	}
}

// truncatePdfCell clips a cell to the column width so an over-long value cannot blow up the table
// layout. The clip is a rough character estimate — exact widths would need font metrics — which is
// why the cap is generous.
func truncatePdfCell(s string, colWidth float64) string {
	max := int(colWidth / 1.1)
	if max < 4 {
		max = 4
	}
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

// ensurePdfSpace starts a new page when the next block (top+needed) would run past the bottom
// margin. Drawing primitives do not trigger fpdf's auto page break — only text cells do — so every
// chart/table block guards itself explicitly.
func ensurePdfSpace(pdf *fpdf.Fpdf, needed float64) {
	if pdf.GetY()+needed > pdfPageBottom {
		pdf.AddPage()
		pdf.SetY(pdfMargin)
	}
}

// sanitizePdfText maps UTF-8 text to WinAnsi-encodable bytes for the PDF core fonts. The core
// fonts (Helvetica) have no Unicode table, so any rune without a cp1252 encoding becomes "?". The
// common typographic runes (curly quotes, dashes, ellipsis, euro) map to their cp1252 bytes rather
// than degrading, which keeps finance-style narrative readable. Full Unicode (CJK, emoji, RTL
// scripts) would require embedding a TTF font — out of scope for this phase and stated honestly.
func sanitizePdfText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if code := winAnsiByte(r); code >= 0 {
			b.WriteByte(byte(code))
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

// winAnsiByte returns the cp1252 (WinAnsi) byte for r, or -1 when r has no WinAnsi encoding. ASCII
// and Latin-1 (0xA0-0xFF) pass through; the common typographic runes map to their cp1252 positions.
func winAnsiByte(r rune) int {
	if r < 0x80 {
		return int(r)
	}
	switch r {
	case '\u20AC':
		return 0x80
	case '\u201A':
		return 0x82
	case '\u0192':
		return 0x83
	case '\u201E':
		return 0x84
	case '\u2026':
		return 0x85
	case '\u2020':
		return 0x86
	case '\u2021':
		return 0x87
	case '\u02C6':
		return 0x88
	case '\u2030':
		return 0x89
	case '\u0160':
		return 0x8A
	case '\u2039':
		return 0x8B
	case '\u0152':
		return 0x8C
	case '\u017D':
		return 0x8E
	case '\u2018':
		return 0x91
	case '\u2019':
		return 0x92
	case '\u201C':
		return 0x93
	case '\u201D':
		return 0x94
	case '\u2022':
		return 0x95
	case '\u2013':
		return 0x96
	case '\u2014':
		return 0x97
	case '\u02DC':
		return 0x98
	case '\u2122':
		return 0x99
	case '\u0161':
		return 0x9A
	case '\u203A':
		return 0x9B
	case '\u0153':
		return 0x9C
	case '\u017E':
		return 0x9E
	case '\u0178':
		return 0x9F
	}
	if r >= 0xA0 && r <= 0xFF {
		return int(r)
	}
	return -1
}
