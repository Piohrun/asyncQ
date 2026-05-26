package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xuri/excelize/v2"
)

func main() {
	root, err := repoRoot()
	must(err)
	output := filepath.Join(root, "demo", "templates", "asyncq-demo-report-template.xlsx")
	must(os.MkdirAll(filepath.Dir(output), 0o755))

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	must(f.SetSheetName("Sheet1", "Dashboard"))
	_, err = f.NewSheet("Summary")
	must(err)
	_, err = f.NewSheet("Trades")
	must(err)
	_, err = f.NewSheet("Notes")
	must(err)

	buildDashboard(f)
	buildDataSheet(f, "Summary", []interface{}{"sym", "lastPrice", "trades"})
	buildDataSheet(f, "Trades", []interface{}{"time", "sym", "price", "size"})
	buildNotes(f)

	idx, err := f.GetSheetIndex("Dashboard")
	must(err)
	f.SetActiveSheet(idx)
	must(f.SaveAs(output))
	fmt.Println(output)
}

func repoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
			return cwd, nil
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return "", fmt.Errorf("could not find repo root")
		}
		cwd = parent
	}
}

func buildDashboard(f *excelize.File) {
	titleStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 18, Color: "1F4E79"}})
	must(err)
	labelStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Color: []string{"EAF2F8"}, Pattern: 1}})
	must(err)
	valueStyle, err := f.NewStyle(&excelize.Style{NumFmt: 2})
	must(err)

	must(f.SetCellValue("Dashboard", "A1", "AsyncQ Demo Market Report"))
	must(f.SetCellStyle("Dashboard", "A1", "A1", titleStyle))
	must(f.SetCellValue("Dashboard", "A2", "This workbook is a template. AsyncQ writes q/Grafana panel data into Summary and Trades. Charts and formulas point at those ranges."))
	must(f.SetCellValue("Dashboard", "A4", "Symbols"))
	must(f.SetCellFormula("Dashboard", "B4", "COUNTA(Summary!A2:A2000)"))
	must(f.SetCellValue("Dashboard", "A5", "Max last price"))
	must(f.SetCellFormula("Dashboard", "B5", "MAX(Summary!B2:B2000)"))
	must(f.SetCellValue("Dashboard", "A6", "Total trades"))
	must(f.SetCellFormula("Dashboard", "B6", "SUM(Summary!C2:C2000)"))
	must(f.SetCellValue("Dashboard", "A7", "Top symbol"))
	must(f.SetCellFormula("Dashboard", "B7", "IFERROR(INDEX(Summary!A2:A2000,MATCH(MAX(Summary!B2:B2000),Summary!B2:B2000,0)),\"\")"))
	must(f.SetCellStyle("Dashboard", "A4", "A7", labelStyle))
	must(f.SetCellStyle("Dashboard", "B5", "B6", valueStyle))
	must(f.SetColWidth("Dashboard", "A", "A", 22))
	must(f.SetColWidth("Dashboard", "B", "B", 20))
	must(f.SetColWidth("Dashboard", "C", "N", 13))

	must(f.AddChart("Dashboard", "A10", &excelize.Chart{
		Type: excelize.Col,
		Series: []excelize.ChartSeries{
			{
				Name:       "Last price",
				Categories: "Summary!$A$2:$A$20",
				Values:     "Summary!$B$2:$B$20",
			},
		},
		Title: []excelize.RichTextRun{{Text: "Last Price by Symbol"}},
		Legend: excelize.ChartLegend{
			Position: "bottom",
		},
		Dimension: excelize.ChartDimension{Width: 520, Height: 300},
	}))
	must(f.AddChart("Dashboard", "I10", &excelize.Chart{
		Type: excelize.Line,
		Series: []excelize.ChartSeries{
			{
				Name:       "Trade price",
				Categories: "Trades!$A$2:$A$51",
				Values:     "Trades!$C$2:$C$51",
			},
		},
		Title: []excelize.RichTextRun{{Text: "Latest Trade Prices"}},
		Legend: excelize.ChartLegend{
			Position: "bottom",
		},
		Dimension: excelize.ChartDimension{Width: 520, Height: 300},
	}))
}

func buildDataSheet(f *excelize.File, sheet string, headers []interface{}) {
	headerStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Color: []string{"1F4E79"}, Pattern: 1}})
	must(err)
	must(f.SetSheetRow(sheet, "A1", &headers))
	must(f.SetCellStyle(sheet, "A1", fmt.Sprintf("%c1", 'A'+len(headers)-1), headerStyle))
	must(f.SetColWidth(sheet, "A", "A", 26))
	must(f.SetColWidth(sheet, "B", "D", 14))
}

func buildNotes(f *excelize.File) {
	rows := [][]interface{}{
		{"Purpose", "Template for testing AsyncQ Excel report generation."},
		{"Summary", "AsyncQ writes latest price rows at Summary!A1."},
		{"Trades", "AsyncQ writes latest trade rows at Trades!A1."},
		{"Dashboard", "Charts and formulas are preconfigured and point at fixed ranges on Summary and Trades."},
	}
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		must(err)
		must(f.SetSheetRow("Notes", cell, &row))
	}
	must(f.SetColWidth("Notes", "A", "A", 18))
	must(f.SetColWidth("Notes", "B", "B", 110))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
