package reports

import (
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/360EntSecGroup-Skylar/excelize"
	"github.com/dude333/rapina/parsers"
	p "github.com/dude333/rapina/parsers"
)

// metric parameters
type metric struct {
	descr  string
	val    float32
	format int // mapped by constants NUMBER, INDEX, PERCENT
}

//
// Report company from DB to Excel
//
func Report(db *sql.DB, company string, path, yamlFile string) (err error) {
	f, err := filename(path, company)
	if err != nil {
		return err
	}

	e := newExcel()
	sheet, _ := e.newSheet(company)

	var lastStatementsRow, lastMetricsRow int

	// Company name
	sheet.mergeCell("A1", "B1")
	sheet.printRows("A1", &[]string{company}, LEFT, true)

	// ACCOUNT NUMBERING AND DESCRIPTION (COLS A AND B) ===============\/

	// Print accounts codes and descriptions in columns A and B
	// starting on row 2. Adjust space related to the group, e.g.:
	// 3.02 ABC <== print in bold if base item and stores the row position in baseItems[]
	//   3.02.01 ABC
	accounts, _ := accountsItems(db, company)
	row := 2
	baseItems := make([]bool, len(accounts)+row)
	for _, it := range accounts {
		var sp string
		sp, baseItems[row] = ident(it.cdConta)
		cell := "A" + strconv.Itoa(row)
		sheet.printRows(cell, &[]string{sp + it.cdConta, sp + it.dsConta}, LEFT, baseItems[row])
		row++
	}
	lastStatementsRow = row - 1
	row += 2
	// Metrics descriptions
	for _, metric := range metricsList(nil) {
		if metric.descr != "" {
			cell := "B" + strconv.Itoa(row)
			sheet.printRows(cell, &[]string{metric.descr}, RIGHT, false)
		}
		row++
	}
	lastMetricsRow = row - 1

	begin, end, err := timeRange(db)
	if err != nil {
		return
	}

	// 	VALUES (COLS C, D, E...) / PER YEAR ===========================\/

	// Print accounts values ONE YEAR PER COLUMN, starting from C, row 2
	var values map[uint32]float32
	cols := "CDEFGHIJKLMONPQRSTUVWXYZ"
	start := begin - 1
	for y := start; y <= end; y++ {
		if y-start >= len(cols) {
			break
		}
		col := string(cols[y-start])
		cell := col + "1"
		sheet.printTitle(cell, "["+strconv.Itoa(y)+"]") // Print year as title in row 1

		values, _ = accountsValues(db, company, y, y == start)
		row = 2
		for _, acct := range accounts {
			cell := col + strconv.Itoa(row)
			sheet.printValue(cell, values[acct.code], NUMBER, baseItems[row])
			row++
		}

		// FINANCIAL METRICS (COLS C, D, E...) / YEAR ===================\/

		// Print financial metrics
		row++
		cell = fmt.Sprintf("%s%d", col, row)
		sheet.printTitle(cell, "["+strconv.Itoa(y)+"]") // Print year as title
		row++

		// Print report in the sequence defined in financialMetricsList()
		for _, metric := range metricsList(values) {
			if metric.format != EMPTY {
				cell := col + strconv.Itoa(row)
				sheet.printValue(cell, metric.val, metric.format, false)
			}
			row++
		}

	} // next year

	_ = lastMetricsRow

	//
	// VERTICAL ANALYSIS
	//
	// CODES | DESCRIPTION | Y1 | Y2 | Yn | sp | v1 | v2 | v3
	//
	wide := (end - begin) + 1
	year := begin
	top := 2
	for col := 2; col <= 2+wide; col++ {
		formulaCol := col + wide + 2
		sheet.printTitle(axis(formulaCol, 1), "'"+strconv.Itoa(year)) // Print year
		year++
		var ref string
		for row := top; row <= lastStatementsRow; row++ {
			idx := row - top
			if idx < 0 || idx >= len(accounts) {
				break
			}
			if len(accounts[idx].cdConta) == 0 {
				break
			}
			n, _ := strconv.Atoi(accounts[idx].cdConta[:1])
			if n > 3 {
				break
			}
			switch accounts[idx].cdConta {
			case "1", "2", "3.01":
				ref = axis(col, row)
			}
			val := axis(col, row)
			formula := fmt.Sprintf(`=IfError(%s/%s, "-")`, val, ref)

			sheet.printFormula(axis(formulaCol, row), formula, PERCENT, baseItems[row])
		}
	}

	sheet.autoWidth()

	sheet2, err := e.newSheet("SETOR")
	if err == nil {
		sheet2.xlsx.SetSheetViewOptions(sheet2.name, 0,
			excelize.ShowGridLines(false),
			excelize.ZoomScale(80),
		)
		sectorReport(db, sheet2, company, yamlFile)
	}

	err = e.saveAndCloseExcel(f)
	if err == nil {
		fmt.Printf("[✓] Dados salvos em %s\n", f)
	}

	return
}

//
// sectorReport gets all the companies related to the 'company' and reports
// their financial summary
//
func sectorReport(db *sql.DB, sheet *Sheet, company, yamlFile string) (err error) {
	var interrupt bool

	// Handle Ctrl+C
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		fmt.Println("\n[ ] Processamento interrompido")
		interrupt = true
	}()

	companies, _ := parsers.FromSector(company, yamlFile)
	if len(companies) <= 1 {
		return
	}

	fmt.Println("[i] Criando relatório setorial (Ctrl+C para interromper)")
	var top, row, col int = 2, 0, 1
	var count int
	for _, co := range companies {
		row = top
		col++
		fmt.Print("[ ] - ", co)
		empty, err := companySummary(db, sheet, &row, &col, count%3 == 0, co)
		ok := "✓"
		if err != nil || empty {
			ok = "x"
			col--
		} else {
			count++
			if count%3 == 0 {
				top = row + 2
				col = 1
			}
		}
		if interrupt {
			return nil
		}
		fmt.Printf("\r[%s\n", ok)
	}

	return
}

//
// companySummary reports all companies from the same segment into the
// 'Setor' sheet.
//
func companySummary(db *sql.DB, sheet *Sheet, row, col *int, printDescr bool, company string) (empty bool, err error) {
	if !isCompany(db, company) {
		return true, nil
	}

	begin, end, err := timeRange(db)
	if err != nil {
		return
	}
	start := begin - 1

	// Formats used in this report
	sTitle := newFormat(DEFAULT, RIGHT, true).newStyle(sheet.xlsx)
	fCompanyName := newFormat(DEFAULT, CENTER, true)
	fCompanyName.size(16)
	sCompanyName := fCompanyName.newStyle(sheet.xlsx)
	//
	fDescr := newFormat(DEFAULT, RIGHT, false)
	fDescr.Border = []formatBorder{{Type: "left", Color: "333333", Style: 1}}
	sDescr := fDescr.newStyle(sheet.xlsx)
	fDescr.Border = []formatBorder{
		{Type: "top", Color: "333333", Style: 1},
		{Type: "left", Color: "333333", Style: 1},
	}
	sDescrTop := fDescr.newStyle(sheet.xlsx)
	fDescr.Border = []formatBorder{
		{Type: "top", Color: "333333", Style: 1},
	}
	sDescrBottom := fDescr.newStyle(sheet.xlsx)

	// Company name
	if printDescr {
		*col++
	}
	sheet.mergeCell(axis(*col, *row), axis(*col+end-begin+1, *row))
	sheet.printCell(*row, *col, company, sCompanyName)
	if printDescr {
		*col--
	}
	*row++

	// Save starting row
	r := *row

	// Set width for the description col
	if printDescr {
		sheet.setColWidth(*col, 18)
		*col++
	}

	// Print values ONE YEAR PER COLUMN
	var values map[uint32]float32
	for y := start; y <= end; y++ {
		values, _ = accountsValues(db, company, y, y == start)

		*row = r

		// Print year
		sheet.printCell(*row, *col, "["+strconv.Itoa(y)+"]", sTitle)
		*row++

		// Print financial metrics
		for i, metric := range metricsList(values) {
			// Description
			if printDescr {
				stl := sDescr
				if i == 0 {
					stl = sDescrTop
				}
				sheet.printCell(*row, *col-1, metric.descr, stl)
			}
			// Values
			if metric.format != EMPTY {
				fVal := newFormat(metric.format, DEFAULT, false)
				fVal.Border = []formatBorder{
					{Type: "top", Color: "cccccc", Style: 1},
					{Type: "right", Color: "cccccc", Style: 1},
					{Type: "bottom", Color: "cccccc", Style: 1},
					{Type: "left", Color: "cccccc", Style: 1},
				}
				stl := fVal.newStyle(sheet.xlsx)
				sheet.printCell(*row, *col, metric.val, stl)
			}
			*row++
		}

		if printDescr {
			sheet.printCell(*row, *col-1, "", sDescrBottom)
		}

		printDescr = false
		*col++
	} // next year

	return
}

//
// metricsList returns the sequence to be printed after the financial statements
//
func metricsList(v map[uint32]float32) (metrics []metric) {
	dividaBruta := v[p.DividaCirc] + v[p.DividaNCirc]
	caixa := v[p.Caixa] + v[p.AplicFinanceiras]
	dividaLiquida := dividaBruta - caixa
	EBITDA := v[p.EBIT] - v[p.Deprec]
	proventos := v[p.Dividendos] + v[p.JurosCapProp]

	var roe float32
	if v[p.LucLiq] > 0 && v[p.Equity] > 0 {
		roe = zeroIfNeg(safeDiv(v[p.LucLiq], v[p.Equity]))
	}

	return []metric{
		{"Patrimônio Líquido", v[p.Equity], NUMBER},
		{"", 0, EMPTY},

		{"Receita Líquida", v[p.Vendas], NUMBER},
		{"EBITDA", EBITDA, NUMBER},
		{"D&A", v[p.Deprec], NUMBER},
		{"EBIT", v[p.EBIT], NUMBER},
		{"Lucro Líquido", v[p.LucLiq], NUMBER},
		{"", 0, EMPTY},

		{"Marg. EBITDA", zeroIfNeg(safeDiv(v[p.EBIT], v[p.Vendas])), PERCENT},
		{"Marg. EBIT", zeroIfNeg(safeDiv(v[p.EBIT], v[p.Vendas])), PERCENT},
		{"Marg. Líq.", zeroIfNeg(safeDiv(v[p.LucLiq], v[p.Vendas])), PERCENT},
		{"ROE", roe, PERCENT},
		{"", 0, EMPTY},

		{"Caixa", caixa, NUMBER},
		{"Dívida Bruta", dividaBruta, NUMBER},
		{"Dívida Líq.", dividaLiquida, NUMBER},
		{"Dív. Bru./PL", zeroIfNeg(safeDiv(dividaBruta, v[p.Equity])), PERCENT},
		{"Dív.Líq./EBITDA", zeroIfNeg(safeDiv(dividaLiquida, EBITDA)), INDEX},
		{"", 0, EMPTY},

		{"FCO", v[p.FCO], NUMBER},
		{"FCI", v[p.FCI], NUMBER},
		{"FCF", v[p.FCF], NUMBER},
		{"Fluxo de Caixa Total", v[p.FCO] + v[p.FCI] + v[p.FCF], NUMBER},
		{"", 0, EMPTY},

		{"Proventos", proventos, NUMBER},
		{"Payout", zeroIfNeg(safeDiv(proventos, v[p.LucLiq])), PERCENT},
	}
}

func zeroIfNeg(n float32) float32 {
	if n < 0 {
		return 0
	}
	return n
}

func safeDiv(n, d float32) float32 {
	if d == 0 {
		return 0
	}
	return n / d
}

//
// ident returns the number of spaces according to the code level, e.g.:
// "1.1 ABC"   => "  " (2 spaces)
// "1.1.1 ABC" => "    " (4 spaces)
// For items equal or above 3, only returns spaces after 2nd level:
// "3.01 ABC"    => ""
// "3.01.01 ABC" => "  "
//
func ident(str string) (spaces string, baseItem bool) {
	num := strings.SplitN(str, ".", 2)[0]
	c := strings.Count(str, ".")
	if num != "1" && num != "2" && c > 0 {
		c--
	}
	if c > 0 {
		spaces = strings.Repeat("  ", c)
	}

	if num == "1" || num == "2" {
		baseItem = c <= 1
	} else {
		baseItem = c == 0
	}

	return
}
