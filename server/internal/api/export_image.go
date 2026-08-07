package api

import (
	"fmt"
	"math"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// Chart-image export (FEAT-040) and the chart embed shared by the Markdown/HTML exports (FEAT-038).
//
// Mechanism decision (recorded for the orchestrator): the exported chart is a SERVER-GENERATED SVG
// that mirrors the in-app first-party ChartRenderer (web/src/components/report/ChartRenderer.tsx) —
// the same 640x320 viewBox, the same padding, the same category/series/scale math, the same tick
// formatting, the same fixed categorical palette. The one deliberate divergence is color: an
// exported image is a file artifact with no CSS theme system, so it uses the canonical LIGHT-mode
// chart token values (globals.css --color-chart-cat-1..8, --color-border, --color-text-secondary)
// via the same [3]int mirrors the PDF uses — exactly the precedent export_pdf.go documents. Dark
// mode is a screen concern, not a file concern.
//
// Geometry is computed in SVG coordinate space using the shared chart helpers (computeChartPoints,
// uniqueSeries, uniqueLabels, scaleMaxFor, pdfFormatTick) and reused by the PPTX deck, which maps
// the same coordinates into EMU space (export_pptx.go). The image export, the HTML/Markdown embed,
// and the deck therefore render the same geometry from the same helpers; the PDF keeps its own
// vector path but mirrors the same ChartRenderer geometry.
//
// A table-type or unsupported chart spec exports an SVG rendering of the result table — the same
// fallback the in-app renderer applies (ChartRenderer's unsupported note → AccessibleDataTable), so
// the Image row never shows a report it cannot serve.

// SVG chart canvas constants, mirroring ChartRenderer's WIDTH/HEIGHT/PAD exactly. Any change here
// must also land in ChartRenderer.tsx and export_pptx.go's mapping — the three renderers are one
// geometry.
const (
	svgWidth      = 640
	svgHeight     = 320
	svgPadTop     = 24
	svgPadRight   = 16
	svgPadBottom  = 40
	svgPadLeft    = 48
	svgPlotWidth  = svgWidth - svgPadLeft - svgPadRight  // 576
	svgPlotHeight = svgHeight - svgPadTop - svgPadBottom // 256
)

// svgTableMaxRow is how many result rows an SVG table (the table-type/unsupported-chart fallback)
// renders before naming the remainder — the same honesty as the PDF table.
const svgTableMaxRow = 14

// buildChartSVG renders a report's chart spec over its execute facts as a standalone SVG document
// (FEAT-040). The output is deterministic and self-contained: concrete colors, no external
// references, so it renders identically in any browser and can be pasted into email or messages.
func buildChartSVG(spec domain.ChartSpec, exec domain.ExecuteStepOutput) ([]byte, error) {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="640" height="320" viewBox="0 0 640 320" role="img" aria-label="`)
	b.WriteString(xmlEscape(chartDisplayTitle(spec)))
	b.WriteString(`">`)
	fmt.Fprintf(&b, `<title>%s</title>`, xmlEscape(chartDisplayTitle(spec)))

	switch spec.ChartType {
	case domain.ChartBar:
		svgBarChart(&b, spec, exec)
	case domain.ChartLine:
		svgLineChart(&b, spec, exec)
	case domain.ChartPie:
		svgPieChart(&b, spec, exec)
	case domain.ChartScatter:
		svgScatterChart(&b, spec, exec)
	default:
		svgTable(&b, exec)
	}

	b.WriteString(`</svg>`)
	return []byte(b.String()), nil
}

// chartDisplayTitle is the chart's display title, defaulting to "Result" when the spec has none
// (mirrors the report view's CardHeader title).
func chartDisplayTitle(spec domain.ChartSpec) string {
	if spec.Title == "" {
		return "Result"
	}
	return spec.Title
}

// rgbToHex renders one of the [3]int color mirrors (pdfChartPalette, pdfTextSecondary, pdfBorder)
// as an SVG `#RRGGBB` fill/stroke value. Going through the existing mirrors keeps the exported
// artifact in lockstep with the PDF's documented light-mode palette instead of introducing a second
// copy of the token values.
func rgbToHex(c [3]int) string {
	return fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2])
}

// xmlEscape makes text safe inside XML element content and attributes. SVG <title>/<text> and PPTX
// <a:t> all need this; the PDF path uses its own WinAnsi sanitizer instead (different encoding).
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// svgAxisGrid emits the four labeled gridlines ChartRenderer's AxisGrid draws: one line per step,
// with a right-aligned tick label to the left of the plot.
func svgAxisGrid(b *strings.Builder, scaleMax float64) {
	border := rgbToHex(pdfBorder)
	secondary := rgbToHex(pdfTextSecondary)
	steps := 4.0
	for i := 0.0; i <= steps; i++ {
		value := scaleMax * i / steps
		y := svgPadTop + svgPlotHeight - (value/scaleMax)*svgPlotHeight
		fmt.Fprintf(b, `<line x1="%d" x2="%d" y1="%.2f" y2="%.2f" stroke="%s" stroke-width="1"/>`,
			svgPadLeft, svgWidth-svgPadRight, y, y, border)
		fmt.Fprintf(b, `<text x="%d" y="%.2f" text-anchor="end" font-size="12" fill="%s">%s</text>`,
			svgPadLeft-6, y+4, secondary, xmlEscape(pdfFormatTick(value)))
	}
}

func svgBarChart(b *strings.Builder, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
	points := computeChartPoints(spec, exec)
	series := uniqueSeries(points)
	categories := uniqueLabels(points)
	if len(categories) == 0 {
		return
	}
	scaleMax := scaleMaxFor(points)
	secondary := rgbToHex(pdfTextSecondary)

	svgAxisGrid(b, scaleMax)

	groupWidth := svgPlotWidth / float64(len(categories))
	barWidth := math.Max(2, groupWidth/math.Max(1, float64(len(series)))-4)

	for ci, category := range categories {
		x := svgPadLeft + float64(ci)*groupWidth
		for si, s := range series {
			val := 0.0
			for _, p := range points {
				if p.label == category && p.series == s && p.hasValue {
					val = p.value
					break
				}
			}
			barHeight := (val / scaleMax) * svgPlotHeight
			color := rgbToHex(pdfChartPalette[si%len(pdfChartPalette)])
			fmt.Fprintf(b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="%s"/>`,
				x+float64(si)*barWidth+2, svgPadTop+svgPlotHeight-barHeight, barWidth, barHeight, color)
		}
		if len(categories) <= 10 {
			fmt.Fprintf(b, `<text x="%.2f" y="%d" text-anchor="middle" font-size="12" fill="%s">%s</text>`,
				x+groupWidth/2, svgPadTop+svgPlotHeight+16, secondary, xmlEscape(category))
		}
	}
}

func svgLineChart(b *strings.Builder, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
	points := computeChartPoints(spec, exec)
	series := uniqueSeries(points)
	if len(series) == 0 {
		return
	}
	scaleMax := scaleMaxFor(points)

	maxX := 0.0
	for _, p := range points {
		if v, ok := pdfToNumber(p.label); ok && v > maxX {
			maxX = v
		}
	}
	useNumericX := maxX > 0

	svgAxisGrid(b, scaleMax)

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
		coords := make([]string, 0, len(pts))
		for i, p := range pts {
			var x float64
			if useNumericX {
				if v, ok := pdfToNumber(p.label); ok {
					x = svgPadLeft + (v/maxX)*svgPlotWidth
				} else {
					continue
				}
			} else {
				x = svgPadLeft + (float64(i)/math.Max(1, float64(len(pts)-1)))*svgPlotWidth
			}
			y := svgPadTop + svgPlotHeight - (p.valueOrZero()/scaleMax)*svgPlotHeight
			coords = append(coords, fmt.Sprintf("%.2f,%.2f", x, y))
		}
		if len(coords) < 2 {
			continue
		}
		color := rgbToHex(pdfChartPalette[si%len(pdfChartPalette)])
		fmt.Fprintf(b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2"/>`,
			strings.Join(coords, " "), color)
	}
}

func svgPieChart(b *strings.Builder, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
	points := computeChartPoints(spec, exec)
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
	if sum == 0 || len(labels) == 0 {
		b.WriteString(`<text x="320" y="160" text-anchor="middle" font-size="14" fill="`)
		b.WriteString(rgbToHex(pdfTextSecondary))
		b.WriteString(`">No numeric values to chart.</text>`)
		return
	}

	cx := float64(svgWidth) / 2
	cy := float64(svgHeight) / 2
	r := math.Min(svgPlotWidth, svgPlotHeight)/2 - 8

	cursor := -math.Pi / 2
	for i, label := range labels {
		sweep := (totals[label] / sum) * math.Pi * 2
		start := cursor
		end := cursor + sweep
		cursor = end
		color := rgbToHex(pdfChartPalette[i%len(pdfChartPalette)])
		fmt.Fprintf(b, `<path d="%s" fill="%s"/>`, svgPiePath(cx, cy, r, start, end), color)
	}
}

// svgPiePath builds a pie-wedge path in the same polar geometry as ChartRenderer's piePath.
func svgPiePath(cx, cy, r, startAngle, endAngle float64) string {
	startX := cx + r*math.Cos(startAngle)
	startY := cy + r*math.Sin(startAngle)
	endX := cx + r*math.Cos(endAngle)
	endY := cy + r*math.Sin(endAngle)
	largeArc := 0
	if endAngle-startAngle > math.Pi {
		largeArc = 1
	}
	return fmt.Sprintf("M %d %d L %.2f %.2f A %.2f %.2f 0 %d 1 %.2f %.2f Z",
		int(math.Round(cx)), int(math.Round(cy)), startX, startY, r, r, largeArc, endX, endY)
}

func svgScatterChart(b *strings.Builder, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
	points := computeChartPoints(spec, exec)
	series := uniqueSeries(points)
	if len(points) == 0 {
		return
	}
	scaleMax := scaleMaxFor(points)

	maxX := 0.0
	for _, p := range points {
		if v, ok := pdfToNumber(p.label); ok && v > maxX {
			maxX = v
		}
	}
	if maxX == 0 {
		maxX = 1
	}

	svgAxisGrid(b, scaleMax)

	for _, p := range points {
		if !p.hasValue {
			continue
		}
		xNum, ok := pdfToNumber(p.label)
		if !ok {
			continue
		}
		x := svgPadLeft + (xNum/maxX)*svgPlotWidth
		y := svgPadTop + svgPlotHeight - (p.value/scaleMax)*svgPlotHeight
		si := indexOfString(series, p.series)
		if si < 0 {
			si = 0
		}
		color := rgbToHex(pdfChartPalette[si%len(pdfChartPalette)])
		fmt.Fprintf(b, `<circle cx="%.2f" cy="%.2f" r="4" fill="%s"/>`, x, y, color)
	}
}

// svgTable renders the executed result set as an SVG table — the image-export equivalent of the
// AccessibleDataTable fallback the in-app renderer uses for table-type or unsupported chart specs.
// Capped at svgTableMaxRow rows; the remainder is named rather than silently dropped.
func svgTable(b *strings.Builder, exec domain.ExecuteStepOutput) {
	if len(exec.Columns) == 0 {
		b.WriteString(`<text x="320" y="160" text-anchor="middle" font-size="14" fill="`)
		b.WriteString(rgbToHex(pdfTextSecondary))
		b.WriteString(`">No result data to chart.</text>`)
		return
	}

	primary := rgbToHex(pdfTextPrimary)
	secondary := rgbToHex(pdfTextSecondary)
	border := rgbToHex(pdfBorder)
	surface := rgbToHex(pdfSurface)

	colCount := len(exec.Columns)
	colWidth := float64(svgWidth) / float64(colCount)
	rowHeight := 22.0
	headerY := 24.0
	rows := len(exec.Rows)
	if rows > svgTableMaxRow {
		rows = svgTableMaxRow
	}

	// Header row.
	for ci, col := range exec.Columns {
		fmt.Fprintf(b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="%s" stroke="%s"/>`,
			float64(ci)*colWidth, headerY, colWidth, rowHeight, surface, border)
		fmt.Fprintf(b, `<text x="%.2f" y="%.2f" font-size="12" font-weight="bold" fill="%s">%s</text>`,
			float64(ci)*colWidth+4, headerY+15, primary, xmlEscape(truncateSvgCell(col, colWidth)))
	}
	// Data rows.
	for ri := 0; ri < rows; ri++ {
		row := exec.Rows[ri]
		y := headerY + rowHeight + float64(ri)*rowHeight
		for ci := 0; ci < colCount; ci++ {
			cell := ""
			if ci < len(row) {
				cell = formatCell(row[ci])
			}
			fmt.Fprintf(b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="none" stroke="%s"/>`,
				float64(ci)*colWidth, y, colWidth, rowHeight, border)
			fmt.Fprintf(b, `<text x="%.2f" y="%.2f" font-size="11" fill="%s">%s</text>`,
				float64(ci)*colWidth+4, y+15, secondary, xmlEscape(truncateSvgCell(cell, colWidth)))
		}
	}
	if len(exec.Rows) > svgTableMaxRow {
		fmt.Fprintf(b, `<text x="%d" y="%.2f" font-size="11" fill="%s">… %d more rows</text>`,
			svgPadLeft, headerY+rowHeight+float64(svgTableMaxRow+1)*rowHeight, secondary,
			len(exec.Rows)-svgTableMaxRow)
	}
}

// truncateSvgCell clips a cell to the column width so an over-long value cannot blow up the table
// layout — the SVG analog of the PDF's truncatePdfCell (rough character estimate, generous cap).
func truncateSvgCell(s string, colWidth float64) string {
	max := int(colWidth / 7)
	if max < 4 {
		max = 4
	}
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
