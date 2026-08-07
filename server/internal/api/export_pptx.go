package api

import (
	"archive/zip"
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// PowerPoint export (FEAT-037, screens/11-export-dialog.md): a real, standard .pptx slide deck with
// charts and narrative arranged per slide, editable in standard presentation software (FEAT-037
// AC2).
//
// Mechanism decision (recorded for the orchestrator): no third-party PPTX library is introduced.
// A .pptx is a ZIP of OOXML parts, and the deck this feature needs is small and well-bounded, so
// the deck is written directly with archive/zip + hand-rolled DrawingML. The chart slide uses
// NATIVE editable shapes (rectangles for bars, freeform polylines for lines, freeform polygons for
// pie wedges, ovals for scatter) in the same geometry ChartRenderer uses, mapped from the shared
// SVG coordinate space in export_image.go — so the exported chart matches the in-app rendering and
// every element stays editable in PowerPoint, Google Slides, and LibreOffice. The result-data slide
// uses a native <a:tbl> table, and narrative/provenance use native text frames. Zero new
// dependencies; the deck is deterministic and testable as a zip.
//
// Slide plan per report:
//
//	1. Title    — report title + generated date
//	2. Narrative — the report's narrative paragraphs
//	3. Chart    — the chart as native shapes; for a table-type/unsupported spec this slide IS the
//	              result table (mirroring the in-app fallback), or an honest note when the execute
//	              output is gone
//	4. Result data — the executed result set as a native table (skipped when the chart slide is the
//	              table or the note)
//	5. Provenance — reconciliation summary, exclusion warnings, and claim-level provenance bullets

// PPTX slide dimensions: 16:9 (13.33" x 7.5") in EMU. All drawing units are EMU (914400 = 1 inch).
const (
	pptxSlideW   = 12192000
	pptxSlideH   = 6858000
	pptxMargin   = 457200
	pptxContentW = pptxSlideW - 2*pptxMargin // 11277600

	// Plot box on the chart slide, in EMU. The geometry itself lives in the shared SVG coordinate
	// space (export_image.go) and is mapped here via pptxPlotX/pptxPlotY.
	pptxPlotLeft = 1500000
	pptxPlotTop  = 2300000
	pptxPlotW    = pptxSlideW - 2*pptxPlotLeft // 9192000
	pptxPlotH    = 2500000
)

// pptxMaxTableRow caps the native result table before the remainder is named (mirrors the PDF).
const pptxMaxTableRow = 14

// pptxColor renders one of the [3]int color mirrors (pdfChartPalette, pdfBorder) as the bare
// 6-hex DrawingML srgbClr value OOXML expects. SVG (export_image.go) needs a "#" prefix; OOXML
// takes bare hex — the two formats use different renderers, so they get different helpers.
func pptxColor(c [3]int) string {
	return fmt.Sprintf("%02X%02X%02X", c[0], c[1], c[2])
}

// buildReportPPTX renders the report as a 16:9 slide deck (FEAT-037).
func buildReportPPTX(report domain.Report, links []domain.ProvenanceLink, exec *domain.ExecuteStepOutput, execAvailable bool) ([]byte, error) {
	var slides []pptxSlide

	slides = append(slides, pptxTitleSlide(report))
	slides = append(slides, pptxNarrativeSlide(report))

	if execAvailable && exec != nil && isRenderableChartType(report.ChartSpec.ChartType) {
		slides = append(slides, pptxChartSlide(report.ChartSpec, *exec))
		slides = append(slides, pptxDataSlide(*exec))
	} else if execAvailable && exec != nil {
		// Table-type (or unsupported) chart spec: the chart slide IS the result table, mirroring
		// the in-app fallback (ChartRenderer's note → AccessibleDataTable). No separate data slide
		// is needed in this case.
		slides = append(slides, pptxDataSlide(*exec))
	} else {
		slides = append(slides, pptxNoteSlide("Chart data unavailable",
			"The chart data for this report is no longer available."))
	}

	slides = append(slides, pptxProvenanceSlide(report, links))

	body, err := writePPTX(slides, report.Title)
	if err != nil {
		return nil, fmt.Errorf("rendering pptx: %w", err)
	}
	return body, nil
}

// isRenderableChartType reports whether the chart spec maps to a drawable graphic in the deck (and
// in the image export). Table and any future spec'd type fall back to the data table — the same
// support boundary as the in-app ChartRenderer.
func isRenderableChartType(t domain.ChartType) bool {
	switch t {
	case domain.ChartBar, domain.ChartLine, domain.ChartPie, domain.ChartScatter:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------------------------
// Slide builders
// ---------------------------------------------------------------------------------------------

func pptxTitleSlide(report domain.Report) pptxSlide {
	b := newPptxSlideBuilder()
	b.addText(pptxMargin, 1800000, pptxContentW, 1000000, []pptxPara{
		textPara(report.Title, 40, true, "111418", "l"),
	})
	b.addText(pptxMargin, 2900000, pptxContentW, 500000, []pptxPara{
		textPara("Generated "+report.CreatedAt.Format("Jan 2, 2006")+" · Groundwork report", 18, false, "5B6270", "l"),
	})
	return pptxSlide{name: "Title", xml: b.finish()}
}

func pptxNarrativeSlide(report domain.Report) pptxSlide {
	b := newPptxSlideBuilder()
	b.addText(pptxMargin, pptxMargin, pptxContentW, 600000, []pptxPara{
		textPara("Narrative", 24, true, "111418", "l"),
	})
	paragraphs := narrativeParagraphs(report.Narrative)
	paras := make([]pptxPara, 0, len(paragraphs))
	for _, p := range paragraphs {
		paras = append(paras, textPara(p, 18, false, "111418", "l"))
	}
	if len(paras) == 0 {
		paras = append(paras, textPara("No narrative was generated for this report.", 18, false, "5B6270", "l"))
	}
	b.addText(pptxMargin, 1300000, pptxContentW, 5000000, paras)
	return pptxSlide{name: "Narrative", xml: b.finish()}
}

func pptxChartSlide(spec domain.ChartSpec, exec domain.ExecuteStepOutput) pptxSlide {
	b := newPptxSlideBuilder()
	b.addText(pptxMargin, pptxMargin, pptxContentW, 700000, []pptxPara{
		textPara(chartDisplayTitle(spec), 26, true, "111418", "l"),
	})
	switch spec.ChartType {
	case domain.ChartBar:
		pptxBarChart(b, spec, exec)
	case domain.ChartLine:
		pptxLineChart(b, spec, exec)
	case domain.ChartPie:
		pptxPieChart(b, spec, exec)
	case domain.ChartScatter:
		pptxScatterChart(b, spec, exec)
	}
	return pptxSlide{name: "Chart", xml: b.finish()}
}

func pptxNoteSlide(title, note string) pptxSlide {
	b := newPptxSlideBuilder()
	b.addText(pptxMargin, pptxMargin, pptxContentW, 700000, []pptxPara{
		textPara(title, 24, true, "111418", "l"),
	})
	b.addText(pptxMargin, 1500000, pptxContentW, 900000, []pptxPara{
		textPara(note, 18, false, "5B6270", "l"),
	})
	return pptxSlide{name: "Note", xml: b.finish()}
}

func pptxDataSlide(exec domain.ExecuteStepOutput) pptxSlide {
	b := newPptxSlideBuilder()
	b.addText(pptxMargin, pptxMargin, pptxContentW, 600000, []pptxPara{
		textPara("Result data", 24, true, "111418", "l"),
	})
	if len(exec.Columns) == 0 {
		b.addText(pptxMargin, 1300000, pptxContentW, 500000, []pptxPara{
			textPara("No result data to show.", 18, false, "5B6270", "l"),
		})
	} else {
		pptxResultTable(b, exec)
	}
	return pptxSlide{name: "Result data", xml: b.finish()}
}

func pptxProvenanceSlide(report domain.Report, links []domain.ProvenanceLink) pptxSlide {
	b := newPptxSlideBuilder()
	b.addText(pptxMargin, pptxMargin, pptxContentW, 600000, []pptxPara{
		textPara("Provenance & reconciliation", 24, true, "111418", "l"),
	})

	var bullets []pptxPara
	bullets = append(bullets, textPara(fmt.Sprintf("Rows read: %d · rows after filtering/grouping: %d",
		report.ReconciliationSummary.RowsIn, report.ReconciliationSummary.RowsOut), 16, false, "111418", "l"))
	for _, w := range report.ReconciliationSummary.ExclusionWarnings {
		bullets = append(bullets, textPara("• "+w, 16, false, "5B6270", "l"))
	}

	if len(links) > 0 {
		bullets = append(bullets, textPara("Provenance-linked claims:", 16, true, "111418", "l"))
		for _, l := range links {
			bullets = append(bullets, textPara(
				fmt.Sprintf("• %s → %s (source rows: %s)", l.ClaimText, l.StatedValue, strings.Join(l.SourceRowIDs, ", ")),
				14, false, "5B6270", "l"))
		}
	} else if len(report.ProvenanceLinks) > 0 {
		bullets = append(bullets, textPara(fmt.Sprintf("Provenance-linked claims: %d", len(report.ProvenanceLinks)), 16, false, "5B6270", "l"))
	} else {
		bullets = append(bullets, textPara("No provenance-linked claims in this report.", 16, false, "5B6270", "l"))
	}

	b.addText(pptxMargin, 1300000, pptxContentW, 5000000, bullets)
	return pptxSlide{name: "Provenance", xml: b.finish()}
}

// ---------------------------------------------------------------------------------------------
// Chart geometry (SVG coordinate space → EMU)
// ---------------------------------------------------------------------------------------------

// pptxPlotX / pptxPlotY map an SVG plot coordinate (relative to the plot area origin) into EMU
// within the deck's plot box. Used for bar/scatter positions, gridlines, and text anchors.
func pptxPlotX(svgX float64) int64 {
	return int64(math.Round((svgX - svgPadLeft) / svgPlotWidth * pptxPlotW))
}

func pptxPlotY(svgY float64) int64 {
	return int64(math.Round((svgY - svgPadTop) / svgPlotHeight * pptxPlotH))
}

// pptxFullX / pptxFullY map a full-viewBox SVG coordinate into the slide content area (used by the
// pie chart, whose circle extends past the plot box in the in-app renderer).
func pptxFullX(svgX float64) int64 {
	return int64(math.Round(svgX / svgWidth * pptxContentW))
}

func pptxFullY(svgY float64) int64 {
	return int64(math.Round(svgY / svgHeight * (pptxSlideH - 2*pptxMargin)))
}

func pptxW(svgW float64) int64 { return int64(math.Round(svgW / svgPlotWidth * pptxPlotW)) }
func pptxH(svgH float64) int64 { return int64(math.Round(svgH / svgPlotHeight * pptxPlotH)) }

// pptxGridlines draws the four labeled gridlines ChartRenderer's AxisGrid draws, as 1pt rects with
// tick-label text boxes to the left of the plot.
func pptxGridlines(b *pptxSlideBuilder, scaleMax float64) {
	border := pptxColor(pdfBorder)
	steps := 4.0
	for i := 0.0; i <= steps; i++ {
		value := scaleMax * i / steps
		y := svgPadTop + svgPlotHeight - (value/scaleMax)*svgPlotHeight
		lineH := int64(12700) // 1pt
		b.addRect(pptxPlotLeft, pptxPlotTop+pptxPlotY(y)-lineH/2, pptxPlotW, lineH, border)
		b.addText(pptxPlotLeft-pptxW(40), pptxPlotTop+pptxPlotY(y)-pptxH(14), pptxW(36), pptxH(20), []pptxPara{
			textPara(pdfFormatTick(value), 11, false, "5B6270", "r"),
		})
	}
}

func pptxBarChart(b *pptxSlideBuilder, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
	points := computeChartPoints(spec, exec)
	series := uniqueSeries(points)
	categories := uniqueLabels(points)
	if len(categories) == 0 {
		return
	}
	scaleMax := scaleMaxFor(points)

	groupWidth := svgPlotWidth / float64(len(categories))
	barWidth := math.Max(2, groupWidth/math.Max(1, float64(len(series)))-4)

	pptxGridlines(b, scaleMax)

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
			barH := (val / scaleMax) * svgPlotHeight
			color := pptxColor(pdfChartPalette[si%len(pdfChartPalette)])
			b.addRect(
				pptxPlotLeft+pptxPlotX(x+float64(si)*barWidth+2),
				pptxPlotTop+pptxPlotY(svgPadTop+svgPlotHeight-barH),
				pptxW(barWidth), pptxH(barH), color)
		}
		if len(categories) <= 10 {
			cx := x + groupWidth/2
			b.addText(
				pptxPlotLeft+pptxPlotX(cx)-pptxW(groupWidth)/2,
				pptxPlotTop+pptxPlotY(svgPadTop+svgPlotHeight+10),
				pptxW(groupWidth), pptxH(22), []pptxPara{
					textPara(category, 11, false, "5B6270", "c"),
				})
		}
	}
}

func pptxLineChart(b *pptxSlideBuilder, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
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

	pptxGridlines(b, scaleMax)

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
			coords = append(coords, fmt.Sprintf("%d,%d",
				int(math.Round(x-svgPadLeft)), int(math.Round(y-svgPadTop))))
		}
		if len(coords) < 2 {
			continue
		}
		color := pptxColor(pdfChartPalette[si%len(pdfChartPalette)])
		// A line series is a no-fill freeform path with the series color as its stroke; the path
		// coordinate space is the plot box (path w/h = svgPlotWidth/svgPlotHeight).
		b.addFreeform(pptxPlotLeft, pptxPlotTop, pptxPlotW, pptxPlotH,
			int(svgPlotWidth), int(svgPlotHeight), coords, "", color, 25400)
	}
}

func pptxPieChart(b *pptxSlideBuilder, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
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
		b.addText(pptxMargin, pptxPlotTop, pptxContentW, 500000, []pptxPara{
			textPara("No numeric values to chart.", 18, false, "5B6270", "l"),
		})
		return
	}

	cx := float64(svgWidth) / 2
	cy := float64(svgHeight) / 2
	r := math.Min(svgPlotWidth, svgPlotHeight)/2 - 8

	// Wedges as filled freeform polygons (center + arc points), each its own shape so each carries
	// its own color. The path space is the full SVG viewBox mapped to the slide content area.
	cursor := -math.Pi / 2
	for i, label := range labels {
		sweep := (totals[label] / sum) * math.Pi * 2
		coords := []string{fmt.Sprintf("%d,%d", int(math.Round(cx)), int(math.Round(cy)))}
		segments := int(sweep/(math.Pi/24)) + 2
		for s := 0; s <= segments; s++ {
			angle := cursor + sweep*float64(s)/float64(segments)
			coords = append(coords, fmt.Sprintf("%d,%d",
				int(math.Round(cx+r*math.Cos(angle))), int(math.Round(cy+r*math.Sin(angle)))))
		}
		color := pptxColor(pdfChartPalette[i%len(pdfChartPalette)])
		b.addFreeform(pptxMargin, pptxMargin, pptxContentW, pptxSlideH-2*pptxMargin,
			svgWidth, svgHeight, coords, color, "", 0)
		cursor += sweep
	}

	// Legend below the pie: swatch + label + share, one line per slice.
	ly := int64(5200000)
	for i, label := range labels {
		if i >= 6 {
			b.addText(pptxMargin+220000, ly-40000, pptxContentW, 300000, []pptxPara{
				textPara(fmt.Sprintf("… and %d more", len(labels)-6), 14, false, "5B6270", "l"),
			})
			break
		}
		share := totals[label] / sum * 100
		b.addRect(pptxMargin, ly, 160000, 160000, pptxColor(pdfChartPalette[i%len(pdfChartPalette)]))
		b.addText(pptxMargin+220000, ly-40000, pptxContentW-220000, 300000, []pptxPara{
			textPara(fmt.Sprintf("%s — %.0f%%", label, share), 14, false, "5B6270", "l"),
		})
		ly += 300000
	}
}

func pptxScatterChart(b *pptxSlideBuilder, spec domain.ChartSpec, exec domain.ExecuteStepOutput) {
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

	pptxGridlines(b, scaleMax)

	dot := pptxW(8) // SVG r=4 → 8-unit diameter
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
		color := pptxColor(pdfChartPalette[si%len(pdfChartPalette)])
		b.addOval(pptxPlotLeft+pptxPlotX(x)-dot/2, pptxPlotTop+pptxPlotY(y)-dot/2, dot, dot, color)
	}
}

// pptxResultTable renders the executed result set as a native PowerPoint table. Capped at
// pptxMaxTableRow rows; the remainder is named rather than silently dropped (same as the PDF).
func pptxResultTable(b *pptxSlideBuilder, exec domain.ExecuteStepOutput) {
	cols := exec.Columns
	rows := len(exec.Rows)
	if rows > pptxMaxTableRow {
		rows = pptxMaxTableRow
	}

	rowH := int64(300000)
	headerH := int64(360000)
	tableW := int64(pptxContentW)
	tableH := headerH + int64(rows)*rowH
	top := int64(1400000)

	grid := make([]string, len(cols))
	for i := range cols {
		grid[i] = fmt.Sprintf(`<a:gridCol w="%d"/>`, tableW/int64(len(cols)))
	}

	var b2 strings.Builder
	b2.WriteString(`<a:tbl><a:tblPr firstRow="1" bandRow="1"/>`)
	b2.WriteString(`<a:tblGrid>`)
	b2.WriteString(strings.Join(grid, ""))
	b2.WriteString(`</a:tblGrid>`)

	// Header row.
	b2.WriteString(fmt.Sprintf(`<a:tr h="%d">`, headerH))
	for _, col := range cols {
		b2.WriteString(`<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="en-US" b="1" sz="1200"><a:solidFill><a:srgbClr val="111418"/></a:solidFill></a:rPr><a:t>`)
		b2.WriteString(xmlEscape(col))
		b2.WriteString(`</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>`)
	}
	b2.WriteString(`</a:tr>`)

	// Data rows.
	for ri := 0; ri < rows; ri++ {
		b2.WriteString(fmt.Sprintf(`<a:tr h="%d">`, rowH))
		row := exec.Rows[ri]
		for ci := 0; ci < len(cols); ci++ {
			cell := ""
			if ci < len(row) {
				cell = formatCell(row[ci])
			}
			b2.WriteString(`<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="en-US" sz="1100"><a:solidFill><a:srgbClr val="5B6270"/></a:solidFill></a:rPr><a:t>`)
			b2.WriteString(xmlEscape(cell))
			b2.WriteString(`</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>`)
		}
		b2.WriteString(`</a:tr>`)
	}
	b2.WriteString(`</a:tbl>`)

	b.addTable(top, tableW, tableH, b2.String())

	if len(exec.Rows) > pptxMaxTableRow {
		b.addText(pptxMargin, top+tableH+100000, pptxContentW, 300000, []pptxPara{
			textPara(fmt.Sprintf("… %d more rows", len(exec.Rows)-pptxMaxTableRow), 12, false, "5B6270", "l"),
		})
	}
}

// ---------------------------------------------------------------------------------------------
// Shape primitives
// ---------------------------------------------------------------------------------------------

type pptxRun struct {
	text  string
	size  int // points
	bold  bool
	color string // 6-hex, no '#'
}

type pptxPara struct {
	runs  []pptxRun
	align string // "l", "c", or "r"
}

func textPara(text string, size int, bold bool, color, align string) pptxPara {
	return pptxPara{runs: []pptxRun{{text: text, size: size, bold: bold, color: color}}, align: align}
}

type pptxSlide struct {
	name string
	xml  string
}

// pptxSlideBuilder accumulates one slide's spTree. Shape ids start at 2 (1 is the group shape).
type pptxSlideBuilder struct {
	b  strings.Builder
	id int
}

func newPptxSlideBuilder() *pptxSlideBuilder {
	b := &pptxSlideBuilder{}
	b.b.WriteString(`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">`)
	b.b.WriteString(`<p:cSld><p:spTree>`)
	b.b.WriteString(`<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>`)
	b.b.WriteString(`<p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`)
	b.id = 2
	return b
}

func (b *pptxSlideBuilder) nextID() int {
	id := b.id
	b.id++
	return id
}

func (b *pptxSlideBuilder) finish() string {
	b.b.WriteString(`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`)
	return b.b.String()
}

func (b *pptxSlideBuilder) addText(x, y, w, h int64, paras []pptxPara) {
	fmt.Fprintf(&b.b, `<p:sp><p:nvSpPr><p:cNvPr id="%d" name="Text"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr/></p:nvSpPr>`, b.nextID())
	fmt.Fprintf(&b.b, `<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>`, x, y, w, h)
	b.b.WriteString(`<p:txBody><a:bodyPr wrap="square" lIns="91440" tIns="45720" rIns="91440" bIns="45720"><a:normAutofit/></a:bodyPr><a:lstStyle/>`)
	for _, para := range paras {
		fmt.Fprintf(&b.b, `<a:p><a:pPr algn="%s"/>`, para.align)
		for _, run := range para.runs {
			fmt.Fprintf(&b.b, `<a:r><a:rPr lang="en-US" sz="%d"`, run.size*100)
			if run.bold {
				b.b.WriteString(` b="1"`)
			}
			fmt.Fprintf(&b.b, `><a:solidFill><a:srgbClr val="%s"/></a:solidFill></a:rPr><a:t>%s</a:t></a:r>`, run.color, xmlEscape(run.text))
		}
		b.b.WriteString(`<a:endParaRPr lang="en-US"/></a:p>`)
	}
	b.b.WriteString(`</p:txBody></p:sp>`)
}

func (b *pptxSlideBuilder) addRect(x, y, w, h int64, fill string) {
	fmt.Fprintf(&b.b, `<p:sp><p:nvSpPr><p:cNvPr id="%d" name="Rect"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>`, b.nextID())
	fmt.Fprintf(&b.b, `<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom>`, x, y, w, h)
	fmt.Fprintf(&b.b, `<a:solidFill><a:srgbClr val="%s"/></a:solidFill></p:spPr>`, fill)
	b.b.WriteString(`<p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:endParaRPr lang="en-US"/></a:p></p:txBody></p:sp>`)
}

func (b *pptxSlideBuilder) addOval(x, y, w, h int64, fill string) {
	fmt.Fprintf(&b.b, `<p:sp><p:nvSpPr><p:cNvPr id="%d" name="Oval"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>`, b.nextID())
	fmt.Fprintf(&b.b, `<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="ellipse"><a:avLst/></a:prstGeom>`, x, y, w, h)
	fmt.Fprintf(&b.b, `<a:solidFill><a:srgbClr val="%s"/></a:solidFill></p:spPr>`, fill)
	b.b.WriteString(`<p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:endParaRPr lang="en-US"/></a:p></p:txBody></p:sp>`)
}

// addFreeform draws a custom-geometry shape whose path points are "x,y" strings in a normalized
// coordinate space of pathW x pathH, mapped to the shape's off/ext box. fill == "" means no fill
// (a pure line); lineColor == "" means no line.
func (b *pptxSlideBuilder) addFreeform(offX, offY, extX, extY int64, pathW, pathH int, points []string, fill, lineColor string, lineW int64) {
	fmt.Fprintf(&b.b, `<p:sp><p:nvSpPr><p:cNvPr id="%d" name="Freeform"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>`, b.nextID())
	fmt.Fprintf(&b.b, `<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm>`, offX, offY, extX, extY)
	fmt.Fprintf(&b.b, `<a:custGeom><a:avLst/><a:gdLst/><a:ahLst/><a:cxnLst/><a:rect l="0" t="0" r="0" b="0"/><a:pathLst><a:path w="%d" h="%d">`, pathW, pathH)
	for i, point := range points {
		parts := strings.SplitN(point, ",", 2)
		if len(parts) != 2 {
			continue
		}
		if i == 0 {
			fmt.Fprintf(&b.b, `<a:moveTo><a:pt x="%s" y="%s"/></a:moveTo>`, parts[0], parts[1])
		} else {
			fmt.Fprintf(&b.b, `<a:lnTo><a:pt x="%s" y="%s"/></a:lnTo>`, parts[0], parts[1])
		}
	}
	b.b.WriteString(`</a:path></a:pathLst></a:custGeom>`)
	if fill == "" {
		b.b.WriteString(`<a:noFill/>`)
	} else {
		fmt.Fprintf(&b.b, `<a:solidFill><a:srgbClr val="%s"/></a:solidFill>`, fill)
	}
	if lineColor != "" {
		fmt.Fprintf(&b.b, `<a:ln w="%d"><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:round/></a:ln>`, lineW, lineColor)
	}
	b.b.WriteString(`</p:spPr><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:endParaRPr lang="en-US"/></a:p></p:txBody></p:sp>`)
}

// addTable embeds a native <a:tbl> body (built by the caller) inside a graphic frame.
func (b *pptxSlideBuilder) addTable(top, tableW, tableH int64, tblXML string) {
	fmt.Fprintf(&b.b, `<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="%d" name="Table"/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr>`, b.nextID())
	fmt.Fprintf(&b.b, `<p:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></p:xfrm>`, pptxMargin, top, tableW, tableH)
	b.b.WriteString(`<a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table">`)
	b.b.WriteString(tblXML)
	b.b.WriteString(`</a:graphicData></a:graphic></p:graphicFrame>`)
}

// ---------------------------------------------------------------------------------------------
// OOXML package assembly
// ---------------------------------------------------------------------------------------------

// writePPTX zips the deck parts into a .pptx file. Entries are written in sorted name order so the
// bytes are deterministic across runs.
func writePPTX(slides []pptxSlide, title string) ([]byte, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	files := map[string]string{
		"[Content_Types].xml":                          pptxContentTypes(len(slides)),
		"_rels/.rels":                                  pptxRootRels,
		"docProps/app.xml":                             fmt.Sprintf(pptxAppProps, len(slides)),
		"docProps/core.xml":                            fmt.Sprintf(pptxCoreProps, xmlEscape(title), now, now),
		"ppt/presentation.xml":                         pptxPresentation(len(slides)),
		"ppt/_rels/presentation.xml.rels":              pptxPresentationRels(len(slides)),
		"ppt/slideMasters/slideMaster1.xml":            pptxSlideMaster,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": pptxSlideMasterRels,
		"ppt/slideLayouts/slideLayout1.xml":            pptxSlideLayout,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": pptxSlideLayoutRels,
		"ppt/theme/theme1.xml":                         pptxTheme,
	}
	for i, slide := range slides {
		files[fmt.Sprintf("ppt/slides/slide%d.xml", i+1)] = slide.xml
		files[fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", i+1)] = pptxSlideRels
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func pptxContentTypes(slideCount int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	b.WriteString(`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`)
	for i := 1; i <= slideCount; i++ {
		fmt.Fprintf(&b, `<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, i)
	}
	b.WriteString(`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>`)
	b.WriteString(`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	b.WriteString(`</Types>`)
	return b.String()
}

const pptxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
	`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
	`</Relationships>`

func pptxPresentation(slideCount int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">`)
	b.WriteString(`<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>`)
	b.WriteString(`<p:sldIdLst>`)
	for i := 1; i <= slideCount; i++ {
		fmt.Fprintf(&b, `<p:sldId id="%d" r:id="rId%d"/>`, 255+i, i+1)
	}
	b.WriteString(`</p:sldIdLst>`)
	b.WriteString(`<p:sldSz cx="12192000" cy="6858000"/>`)
	b.WriteString(`<p:notesSz cx="6858000" cy="9144000"/>`)
	b.WriteString(`<p:defaultTextStyle><a:defPPr><a:defRPr lang="en-US"/></a:defPPr></p:defaultTextStyle>`)
	b.WriteString(`</p:presentation>`)
	return b.String()
}

func pptxPresentationRels(slideCount int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	b.WriteString(`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>`)
	for i := 1; i <= slideCount; i++ {
		fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, i+1, i)
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

const pptxSlideMaster = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
	`<p:cSld><p:bg><p:bgPr><a:solidFill><a:schemeClr val="lt1"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>` +
	`<p:spTree>` +
	`<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>` +
	`<p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>` +
	`<p:sp><p:nvSpPr><p:cNvPr id="2" name="Title Placeholder 1"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:endParaRPr lang="en-US"/></a:p></p:txBody></p:sp>` +
	`<p:sp><p:nvSpPr><p:cNvPr id="3" name="Content Placeholder 2"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph idx="1"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:endParaRPr lang="en-US"/></a:p></p:txBody></p:sp>` +
	`</p:spTree></p:cSld>` +
	`<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>` +
	`<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst>` +
	`<p:txStyles>` +
	`<p:titleStyle><a:lvl1pPr><a:defRPr sz="4400" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl1pPr><a:lvl2pPr><a:defRPr sz="3200" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl2pPr><a:lvl3pPr><a:defRPr sz="2800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl3pPr><a:lvl4pPr><a:defRPr sz="2400" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl4pPr><a:lvl5pPr><a:defRPr sz="2200" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl5pPr><a:lvl6pPr><a:defRPr sz="2000" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl6pPr><a:lvl7pPr><a:defRPr sz="2000" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl7pPr><a:lvl8pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl8pPr><a:lvl9pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl9pPr></p:titleStyle>` +
	`<p:bodyStyle><a:lvl1pPr><a:defRPr sz="2400" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl1pPr><a:lvl2pPr><a:defRPr sz="2200" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl2pPr><a:lvl3pPr><a:defRPr sz="2000" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl3pPr><a:lvl4pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl4pPr><a:lvl5pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl5pPr><a:lvl6pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl6pPr><a:lvl7pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl7pPr><a:lvl8pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl8pPr><a:lvl9pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl9pPr></p:bodyStyle>` +
	`<p:otherStyle><a:lvl1pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl1pPr><a:lvl2pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl2pPr><a:lvl3pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl3pPr><a:lvl4pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl4pPr><a:lvl5pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl5pPr><a:lvl6pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl6pPr><a:lvl7pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl7pPr><a:lvl8pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl8pPr><a:lvl9pPr><a:defRPr sz="1800" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/></a:defRPr></a:lvl9pPr></p:otherStyle>` +
	`</p:txStyles></p:sldMaster>`

const pptxSlideMasterRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>` +
	`</Relationships>`

const pptxSlideLayout = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="blank" preserve="1">` +
	`<p:cSld name="Blank"><p:spTree>` +
	`<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>` +
	`<p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>` +
	`</p:spTree></p:cSld>` +
	`<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>` +
	`</p:sldLayout>`

const pptxSlideLayoutRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/>` +
	`</Relationships>`

const pptxSlideRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>` +
	`</Relationships>`

const pptxTheme = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Groundwork">` +
	`<a:themeElements>` +
	`<a:clrScheme name="Groundwork">` +
	`<a:dk1><a:srgbClr val="111418"/></a:dk1>` +
	`<a:lt1><a:srgbClr val="FFFFFF"/></a:lt1>` +
	`<a:dk2><a:srgbClr val="1F2933"/></a:dk2>` +
	`<a:lt2><a:srgbClr val="F4F5F7"/></a:lt2>` +
	`<a:accent1><a:srgbClr val="1D4ED8"/></a:accent1>` +
	`<a:accent2><a:srgbClr val="B45309"/></a:accent2>` +
	`<a:accent3><a:srgbClr val="0F766E"/></a:accent3>` +
	`<a:accent4><a:srgbClr val="7C3AED"/></a:accent4>` +
	`<a:accent5><a:srgbClr val="BE185D"/></a:accent5>` +
	`<a:accent6><a:srgbClr val="4D7C0F"/></a:accent6>` +
	`<a:hlink><a:srgbClr val="1D4ED8"/></a:hlink>` +
	`<a:folHlink><a:srgbClr val="0F766E"/></a:folHlink>` +
	`</a:clrScheme>` +
	`<a:fontScheme name="Groundwork">` +
	`<a:majorFont><a:latin typeface="Calibri Light"/><a:ea typeface=""/><a:cs typeface=""/></a:majorFont>` +
	`<a:minorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface=""/></a:minorFont>` +
	`</a:fontScheme>` +
	`<a:fmtScheme name="Groundwork">` +
	`<a:fillStyleLst>` +
	`<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>` +
	`<a:gradFill rotWithShape="1"><a:gsLst><a:gs pos="0"><a:schemeClr val="phClr"><a:tint val="50000"/><a:satMod val="300000"/></a:schemeClr></a:gs><a:gs pos="35000"><a:schemeClr val="phClr"><a:tint val="37000"/><a:satMod val="300000"/></a:schemeClr></a:gs><a:gs pos="100000"><a:schemeClr val="phClr"><a:tint val="15000"/><a:satMod val="350000"/></a:schemeClr></a:gs></a:gsLst><a:lin ang="16200000" scaled="1"/></a:gradFill>` +
	`<a:gradFill rotWithShape="1"><a:gsLst><a:gs pos="0"><a:schemeClr val="phClr"><a:shade val="51000"/><a:satMod val="130000"/></a:schemeClr></a:gs><a:gs pos="80000"><a:schemeClr val="phClr"><a:shade val="93000"/><a:satMod val="130000"/></a:schemeClr></a:gs><a:gs pos="100000"><a:schemeClr val="phClr"><a:shade val="94000"/><a:satMod val="135000"/></a:schemeClr></a:gs></a:gsLst><a:lin ang="16200000" scaled="0"/></a:gradFill>` +
	`</a:fillStyleLst>` +
	`<a:lnStyleLst>` +
	`<a:ln w="9525" cap="flat" cmpd="sng" algn="ctr"><a:solidFill><a:schemeClr val="phClr"><a:shade val="95000"/><a:satMod val="105000"/></a:schemeClr></a:solidFill><a:prstDash val="solid"/></a:ln>` +
	`<a:ln w="25400" cap="flat" cmpd="sng" algn="ctr"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:prstDash val="solid"/></a:ln>` +
	`<a:ln w="38100" cap="flat" cmpd="sng" algn="ctr"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:prstDash val="solid"/></a:ln>` +
	`</a:lnStyleLst>` +
	`<a:effectStyleLst>` +
	`<a:effectStyle><a:effectLst/></a:effectStyle>` +
	`<a:effectStyle><a:effectLst/></a:effectStyle>` +
	`<a:effectStyle><a:effectLst/></a:effectStyle>` +
	`</a:effectStyleLst>` +
	`<a:bgFillStyleLst>` +
	`<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>` +
	`<a:solidFill><a:schemeClr val="phClr"><a:tint val="95000"/><a:satMod val="170000"/></a:schemeClr></a:solidFill>` +
	`<a:gradFill rotWithShape="1"><a:gsLst><a:gs pos="0"><a:schemeClr val="phClr"><a:tint val="93000"/><a:satMod val="150000"/><a:shade val="98000"/><a:lumMod val="102000"/></a:schemeClr></a:gs><a:gs pos="50000"><a:schemeClr val="phClr"><a:tint val="98000"/><a:satMod val="130000"/><a:shade val="90000"/><a:lumMod val="103000"/></a:schemeClr></a:gs><a:gs pos="100000"><a:schemeClr val="phClr"><a:shade val="63000"/><a:satMod val="120000"/></a:schemeClr></a:gs></a:gsLst><a:lin ang="16200000" scaled="0"/></a:gradFill>` +
	`</a:bgFillStyleLst>` +
	`</a:fmtScheme>` +
	`</a:themeElements></a:theme>`

const pptxCoreProps = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
	`<dc:title>%s</dc:title>` +
	`<dc:creator>Groundwork</dc:creator>` +
	`<cp:lastModifiedBy>Groundwork</cp:lastModifiedBy>` +
	`<dcterms:created xsi:type="dcterms:W3CDTF">%s</dcterms:created>` +
	`<dcterms:modified xsi:type="dcterms:W3CDTF">%s</dcterms:modified>` +
	`</cp:coreProperties>`

const pptxAppProps = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">` +
	`<Application>Groundwork</Application>` +
	`<Slides>%d</Slides>` +
	`</Properties>`
