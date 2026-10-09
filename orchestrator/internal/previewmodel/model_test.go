package previewmodel

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func document(t *testing.T, body string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "source.docx")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`))
	if err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return name
}
func TestLargeDocumentReadingReachesLastTableRow(t *testing.T) {
	var body strings.Builder
	body.WriteString(`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Research</w:t></w:r></w:p><w:tbl>`)
	for row := 0; row < 3000; row++ {
		body.WriteString(`<w:tr>`)
		for cell := 0; cell < 5; cell++ {
			fmt.Fprintf(&body, `<w:tc><w:p><w:r><w:t>Row %d cell %d</w:t></w:r></w:p></w:tc>`, row, cell)
		}
		body.WriteString(`</w:tr>`)
	}
	body.WriteString(`</w:tbl>`)
	output := t.TempDir()
	if err := Build(context.Background(), document(t, body.String()), output, "docx"); err != nil {
		t.Fatal(err)
	}
	db, err := Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manifest, err := ReadManifest(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Coverage != "complete" || manifest.Sheets[0].Count != 3001 || len(manifest.Headings) != 1 {
		t.Fatal(manifest)
	}
	window, err := ReadWindow(context.Background(), db, 0, 2999, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Items) != 2 || window.Items[1].Cells[4].Text != "Row 2999 cell 4" {
		t.Fatal(window)
	}
	found, err := Find(context.Background(), db, 0, 0, "Row 2999 cell 4")
	if err != nil || found != 3000 {
		t.Fatal(found, err)
	}
}
func TestLargeWorkbookUsesSparseRowsAndSavedFormatting(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.xlsx")
	book := excelize.NewFile()
	stream, err := book.NewStreamWriter("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	for row := 1; row <= 22962; row++ {
		cells := make([]interface{}, 12)
		for col := range cells {
			cells[col] = fmt.Sprintf("Cell %d:%d", row, col)
		}
		if err = stream.SetRow(fmt.Sprintf("A%d", row), cells); err != nil {
			t.Fatal(err)
		}
	}
	if err = stream.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = book.SaveAs(source); err != nil {
		t.Fatal(err)
	}
	book.Close()
	output := t.TempDir()
	if err = Build(context.Background(), source, output, "xlsx"); err != nil {
		t.Fatal(err)
	}
	db, err := Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manifest, err := ReadManifest(context.Background(), db)
	if err != nil || manifest.Sheets[0].Count != 22962 || manifest.Sheets[0].Columns != 12 {
		t.Fatal(manifest, err)
	}
	last, err := ReadWindow(context.Background(), db, 0, 22961, 1)
	if err != nil || last.Items[0].Cells[11].Text != "Cell 22962:11" {
		t.Fatal(last, err)
	}
	found, err := Find(context.Background(), db, 0, 0, "Cell 22962:11")
	if err != nil || found != 22961 {
		t.Fatal(found, err)
	}
}
func TestSparseWorkbookMergeFreezeAndFormula(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.xlsx")
	book := excelize.NewFile()
	book.SetCellValue("Sheet1", "A1", "First")
	book.SetCellFormula("Sheet1", "Z1000", "SUM(A2:A3)")
	book.MergeCell("Sheet1", "A1", "B1")
	book.SetPanes("Sheet1", &excelize.Panes{Freeze: true, XSplit: 1, YSplit: 1, TopLeftCell: "B2"})
	book.NewSheet("Hidden")
	book.SetSheetVisible("Hidden", false)
	if err := book.SaveAs(source); err != nil {
		t.Fatal(err)
	}
	book.Close()
	output := t.TempDir()
	if err := Build(context.Background(), source, output, "xlsx"); err != nil {
		t.Fatal(err)
	}
	db, err := Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manifest, err := ReadManifest(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Sheets[0].Count != 2 || manifest.Sheets[0].Columns != 26 || manifest.Sheets[0].FrozenRows != 1 || len(manifest.Sheets[0].Merges) != 1 || !manifest.Sheets[1].Hidden {
		t.Fatal(manifest)
	}
	window, err := ReadWindow(context.Background(), db, 0, 1, 1)
	if err != nil || window.Items[0].Row != 1000 || window.Items[0].Cells[0].Text != "=SUM(A2:A3)" {
		t.Fatal(window, err)
	}
	position, err := LocateRow(context.Background(), db, 0, 1000)
	if err != nil || position != 1 {
		t.Fatal(position, err)
	}
	tile, err := ReadTile(context.Background(), db, 0, 0, 100, 25, 1)
	if err != nil || len(tile.Items[0].Cells) != 0 || len(tile.Items[1].Cells) != 1 || tile.Items[1].Cells[0].Column != 25 {
		t.Fatal(tile, err)
	}
	_, err = ReadTile(context.Background(), db, 0, 0, 100, 0, 65)
	if err != ErrInvalid {
		t.Fatal("unbounded tile admitted", err)
	}
}
func TestRejectEntitiesBeforeModelAndHonorCancellation(t *testing.T) {
	if err := Build(context.Background(), document(t, `<!DOCTYPE x [<!ENTITY y "bad">]><w:p/>`), t.TempDir(), "docx"); err != ErrInvalid {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Build(canceled, document(t, `<w:p/>`), t.TempDir(), "docx"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestNestedAndMergedDocumentTablesKeepAllText(t *testing.T) {
	body := `<w:tbl><w:tr><w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t>Outer</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Nested value</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:tc><w:tc><w:p><w:r><w:t>Final cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	out := t.TempDir()
	if err := Build(context.Background(), document(t, body), out, "docx"); err != nil {
		t.Fatal(err)
	}
	db, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	window, err := ReadWindow(context.Background(), db, 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if window.Items[0].Cells[0].Text != "Outer\nNested value" || window.Items[0].Cells[1].Column != 2 {
		t.Fatal(window)
	}
}
