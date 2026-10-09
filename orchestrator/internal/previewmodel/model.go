// Package previewmodel derives bounded, read-only Office reading indexes. The
// application runs Build in an isolated child; requests only query completed data.
package previewmodel

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	_ "modernc.org/sqlite"
)

const Version = "office-reading-1"
const MaxOutputBytes int64 = 128 << 20

type Sheet struct {
	Name          string   `json:"name"`
	Count         int      `json:"count"`
	Columns       int      `json:"columns"`
	Hidden        bool     `json:"hidden"`
	Merges        []string `json:"merges,omitempty"`
	FrozenRows    int      `json:"frozen_rows,omitempty"`
	FrozenColumns int      `json:"frozen_columns,omitempty"`
}
type Heading struct {
	Position int    `json:"position"`
	Text     string `json:"text"`
}
type Manifest struct {
	Version  string    `json:"version"`
	Kind     string    `json:"kind"`
	Coverage string    `json:"coverage"`
	Sheets   []Sheet   `json:"sheets"`
	Headings []Heading `json:"headings"`
	Notices  []string  `json:"notices"`
}
type Cell struct {
	Column int    `json:"column"`
	Text   string `json:"text"`
	Span   int    `json:"span,omitempty"`
	Merge  string `json:"merge,omitempty"`
}
type Item struct {
	Position int    `json:"position"`
	Row      int    `json:"row"`
	Text     string `json:"text,omitempty"`
	Cells    []Cell `json:"cells,omitempty"`
	Heading  bool   `json:"heading,omitempty"`
}
type Window struct {
	Digest   string `json:"digest"`
	Start    int    `json:"start"`
	Total    int    `json:"total"`
	Coverage string `json:"coverage"`
	Items    []Item `json:"items"`
}

var ErrInvalid = errors.New("container_invalid")
var ErrLimit = errors.New("content_limit")
var ErrUnsupported = errors.New("unsupported_feature")

func charsetReader(name string, input io.Reader) (io.Reader, error) {
	return charset.NewReaderLabel(name, input)
}

func xmlDecoder(input io.Reader) *xml.Decoder {
	buffer := bufio.NewReader(input)
	prefix, _ := buffer.Peek(2)
	utf16 := len(prefix) == 2 && (prefix[0] == 0xff && prefix[1] == 0xfe || prefix[0] == 0xfe && prefix[1] == 0xff)
	var reader io.Reader = buffer
	if utf16 {
		order := unicode.LittleEndian
		if prefix[0] == 0xfe {
			order = unicode.BigEndian
		}
		reader = transform.NewReader(buffer, unicode.UTF16(order, unicode.ExpectBOM).NewDecoder())
	}
	decoder := xml.NewDecoder(reader)
	decoder.CharsetReader = func(name string, input io.Reader) (io.Reader, error) {
		if utf16 && strings.HasPrefix(strings.ToLower(name), "utf-16") {
			return input, nil
		}
		return charsetReader(name, input)
	}
	return decoder
}

// Validate every ZIP entry, including optional parts, before a third-party
// reader can decompress it. Reading-model limits are separate from visual DOM.
func validate(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil {
		return ErrInvalid
	}
	if len(archive.File) > 4096 {
		return ErrLimit
	}
	names := map[string]bool{}
	var expanded uint64
	nodes := 0
	for _, entry := range archive.File {
		name := entry.Name
		clean := path.Clean(name)
		if strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.TrimSuffix(name, "/") != clean || names[strings.ToLower(clean)] || entry.Flags&1 != 0 || (entry.Method != zip.Store && entry.Method != zip.Deflate) {
			return ErrInvalid
		}
		names[strings.ToLower(clean)] = true
		if entry.UncompressedSize64 > 64<<20 {
			return ErrLimit
		}
		expanded += entry.UncompressedSize64
		if expanded > 256<<20 {
			return ErrLimit
		}
		reader, err := entry.Open()
		if err != nil {
			return ErrInvalid
		}
		var read int64
		if strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".rels") {
			decoder := xmlDecoder(io.TeeReader(io.LimitReader(reader, 64<<20+1), counterWriter{&read}))
			depth := 0
			for {
				token, e := decoder.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					reader.Close()
					return ErrInvalid
				}
				switch token := token.(type) {
				case xml.StartElement:
					depth++
					nodes++
					if depth > 64 || nodes > 4_000_000 {
						reader.Close()
						return ErrLimit
					}
					if token.Name.Local == "altChunk" {
						reader.Close()
						return ErrUnsupported
					}
				case xml.EndElement:
					depth--
				case xml.Directive:
					if strings.Contains(strings.ToUpper(string(token)), "DOCTYPE") {
						reader.Close()
						return ErrInvalid
					}
				}
			}
		} else {
			read, err = io.Copy(io.Discard, io.LimitReader(reader, 64<<20+1))
		}
		reader.Close()
		if err != nil || uint64(read) != entry.UncompressedSize64 {
			return ErrInvalid
		}
	}
	return nil
}

type counterWriter struct{ n *int64 }

func (c counterWriter) Write(p []byte) (int, error) { *c.n += int64(len(p)); return len(p), nil }

func Build(ctx context.Context, source, output, kind string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = validate(file); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", filepath.Join(output, "model.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `PRAGMA journal_mode=OFF; PRAGMA cache_size=-4096; CREATE TABLE items(sheet INTEGER,position INTEGER,row INTEGER,text TEXT,data TEXT,PRIMARY KEY(sheet,position)); CREATE INDEX item_rows ON items(sheet,row); CREATE TABLE metadata(data TEXT);`)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO items VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer statement.Close()
	manifest := Manifest{Version: Version, Kind: kind, Coverage: "complete", Sheets: []Sheet{}, Headings: []Heading{}, Notices: []string{}}
	var total int64
	put := func(sheet int, item Item) error {
		data, e := json.Marshal(item)
		if e != nil {
			return e
		}
		total += int64(len(data))
		if len(data) > 256<<10 || total > 96<<20 {
			return ErrLimit
		}
		text := item.Text
		for _, cell := range item.Cells {
			text += "\t" + cell.Text
		}
		_, e = statement.ExecContext(ctx, sheet, item.Position, item.Row, text, string(data))
		return e
	}
	switch kind {
	case "docx":
		err = buildDocument(ctx, file, &manifest, put)
	case "xlsx":
		err = buildWorkbook(ctx, source, &manifest, put)
	default:
		err = ErrUnsupported
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO metadata VALUES(?)`, string(data)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(output, "model.sqlite"))
	if err != nil {
		return err
	}
	if info.Size() > MaxOutputBytes {
		return ErrLimit
	}
	return nil
}

func buildWorkbook(ctx context.Context, source string, manifest *Manifest, put func(int, Item) error) error {
	book, err := excelize.OpenFile(source, excelize.Options{UnzipSizeLimit: 256 << 20, UnzipXMLSizeLimit: 1 << 20})
	if err != nil {
		return ErrInvalid
	}
	defer book.Close()
	names := book.GetSheetList()
	if len(names) > 64 {
		return ErrLimit
	}
	cells := 0
	for index, name := range names {
		if err = ctx.Err(); err != nil {
			return err
		}
		visible, e := book.GetSheetVisible(name)
		if e != nil {
			return ErrInvalid
		}
		sheet := Sheet{Name: name, Hidden: !visible}
		formulas, e := sheetMetadata(source, index, &sheet)
		if e != nil {
			return e
		}
		rows, e := book.Rows(name)
		if e != nil {
			return ErrInvalid
		}
		rowNumber := 0
		for rows.Next() {
			rowNumber++
			if rowNumber > 1_048_576 {
				rows.Close()
				return ErrLimit
			}
			if err = ctx.Err(); err != nil {
				rows.Close()
				return err
			}
			values, e := rows.Columns()
			if e != nil {
				rows.Close()
				return ErrInvalid
			}
			item := Item{Position: sheet.Count, Row: rowNumber, Cells: []Cell{}}
			for col, formula := range formulas[rowNumber] {
				for len(values) <= col {
					values = append(values, "")
				}
				if values[col] == "" {
					values[col] = "=" + formula
				}
			}
			for col, value := range values {
				if value != "" {
					item.Cells = append(item.Cells, Cell{Column: col, Text: value})
					sheet.Columns = max(sheet.Columns, col+1)
					cells++
				}
			}
			if cells > 1_000_000 || len(values) > 16_384 {
				rows.Close()
				return ErrLimit
			}
			if len(item.Cells) == 0 {
				continue
			}
			if err = put(index, item); err != nil {
				rows.Close()
				return err
			}
			sheet.Count++
			if sheet.Count > 100_000 {
				rows.Close()
				return ErrLimit
			}
		}
		if err = rows.Error(); err != nil {
			rows.Close()
			return ErrInvalid
		}
		rows.Close()
		manifest.Sheets = append(manifest.Sheets, sheet)
	}
	manifest.Notices = []string{"saved_values", "drawings_omitted"}
	return nil
}

// Read only merge/pane metadata and uncached formulas. Public convenience APIs
// for these values materialize complete worksheets, defeating streaming rows.
func sheetMetadata(source string, index int, sheet *Sheet) (map[int]map[int]string, error) {
	archive, err := zip.OpenReader(source)
	if err != nil {
		return nil, ErrInvalid
	}
	defer archive.Close()
	readXML := func(name string) (*xml.Decoder, io.Closer, error) {
		for _, f := range archive.File {
			if f.Name == name {
				r, e := f.Open()
				if e != nil {
					return nil, nil, e
				}
				d := xmlDecoder(r)
				return d, r, nil
			}
		}
		return nil, nil, ErrInvalid
	}
	decoder, closer, err := readXML("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	id := ""
	at := 0
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			closer.Close()
			return nil, ErrInvalid
		}
		if t, ok := token.(xml.StartElement); ok && t.Name.Local == "sheet" {
			if at == index {
				for _, a := range t.Attr {
					if a.Name.Local == "id" {
						id = a.Value
					}
				}
			}
			at++
		}
	}
	closer.Close()
	if id == "" {
		return nil, ErrInvalid
	}
	decoder, closer, err = readXML("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	target := ""
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			closer.Close()
			return nil, ErrInvalid
		}
		if t, ok := token.(xml.StartElement); ok && t.Name.Local == "Relationship" {
			attrs := map[string]string{}
			for _, a := range t.Attr {
				attrs[a.Name.Local] = a.Value
			}
			if attrs["Id"] == id {
				if attrs["TargetMode"] == "External" {
					closer.Close()
					return nil, ErrUnsupported
				}
				target = attrs["Target"]
			}
		}
	}
	closer.Close()
	if strings.HasPrefix(target, "/") {
		target = strings.TrimPrefix(target, "/")
	} else {
		target = path.Join("xl", target)
	}
	if !strings.HasPrefix(target, "xl/") {
		return nil, ErrInvalid
	}
	decoder, closer, err = readXML(target)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	formulas := map[int]map[int]string{}
	formulaCount, formulaBytes := 0, 0
	axis, formula, field := "", "", ""
	cached := false
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, ErrInvalid
		}
		switch t := token.(type) {
		case xml.StartElement:
			attrs := map[string]string{}
			for _, a := range t.Attr {
				attrs[a.Name.Local] = a.Value
			}
			switch t.Name.Local {
			case "mergeCell":
				if len(sheet.Merges) >= 10_000 {
					return nil, ErrLimit
				}
				sheet.Merges = append(sheet.Merges, attrs["ref"])
			case "pane":
				if strings.HasPrefix(attrs["state"], "frozen") {
					fmt.Sscan(attrs["ySplit"], &sheet.FrozenRows)
					fmt.Sscan(attrs["xSplit"], &sheet.FrozenColumns)
				}
			case "c":
				axis = attrs["r"]
				formula = ""
				cached = false
			case "f", "v":
				field = t.Name.Local
			}
		case xml.CharData:
			if field == "f" {
				formula += string(t)
				if len(formula) > 64<<10 {
					return nil, ErrLimit
				}
			} else if field == "v" && len(t) > 0 {
				cached = true
			}
		case xml.EndElement:
			if t.Name.Local == "f" || t.Name.Local == "v" {
				field = ""
			}
			if t.Name.Local == "c" && formula != "" && !cached {
				formulaCount++
				formulaBytes += len(formula)
				if formulaCount > 1_000_000 || formulaBytes > 16<<20 {
					return nil, ErrLimit
				}
				col, row, e := excelize.CellNameToCoordinates(axis)
				if e != nil {
					return nil, ErrInvalid
				}
				if formulas[row] == nil {
					formulas[row] = map[int]string{}
				}
				formulas[row][col-1] = formula
			}
		}
	}
	return formulas, nil
}

func buildDocument(ctx context.Context, file *os.File, manifest *Manifest, put func(int, Item) error) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil {
		return ErrInvalid
	}
	sheet := Sheet{Name: "Document", Columns: 1}
	found := false
	parts := map[string]*zip.File{}
	for _, entry := range archive.File {
		parts[entry.Name] = entry
	}
	for _, name := range []string{"word/document.xml", "word/footnotes.xml", "word/endnotes.xml"} {
		entry := parts[name]
		if entry == nil {
			continue
		}
		if entry.Name == "word/document.xml" {
			found = true
		}
		reader, e := entry.Open()
		if e != nil {
			return ErrInvalid
		}
		decoder := xmlDecoder(reader)
		var paragraph strings.Builder
		var row *Item
		var cell *Cell
		var nested []struct {
			row  *Item
			cell *Cell
		}
		inText, heading := false, false
		emit := func(item Item) error {
			item.Position = sheet.Count
			item.Row = sheet.Count + 1
			if sheet.Count >= 100_000 {
				return ErrLimit
			}
			if item.Heading && len(manifest.Headings) < 2048 {
				manifest.Headings = append(manifest.Headings, Heading{Position: item.Position, Text: item.Text})
			}
			if e := put(0, item); e != nil {
				return e
			}
			sheet.Count++
			return nil
		}
		for {
			if e = ctx.Err(); e != nil {
				reader.Close()
				return e
			}
			token, e := decoder.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				reader.Close()
				return ErrInvalid
			}
			switch t := token.(type) {
			case xml.StartElement:
				if t.Name.Space != "http://schemas.openxmlformats.org/wordprocessingml/2006/main" {
					continue
				}
				switch t.Name.Local {
				case "tr":
					if row != nil {
						if cell == nil {
							reader.Close()
							return ErrInvalid
						}
						nested = append(nested, struct {
							row  *Item
							cell *Cell
						}{row, cell})
						cell = nil
					}
					row = &Item{Cells: []Cell{}}
				case "tc":
					if row == nil {
						reader.Close()
						return ErrInvalid
					}
					column := 0
					for _, existing := range row.Cells {
						column += max(1, existing.Span)
					}
					cell = &Cell{Column: column, Span: 1}
				case "p":
					paragraph.Reset()
					heading = false
				case "t":
					inText = true
				case "tab":
					paragraph.WriteString("\t")
				case "br", "cr":
					paragraph.WriteString("\n")
				case "pStyle":
					for _, a := range t.Attr {
						if a.Name.Local == "val" {
							heading = strings.HasPrefix(strings.ToLower(a.Value), "heading") || a.Value == "Title"
						}
					}
				case "gridSpan":
					if cell != nil {
						for _, a := range t.Attr {
							if a.Name.Local == "val" {
								_, e = fmt.Sscan(a.Value, &cell.Span)
								if e != nil || cell.Span < 1 || cell.Span > 256 {
									reader.Close()
									return ErrInvalid
								}
							}
						}
					}
				case "vMerge":
					if cell != nil {
						cell.Merge = "continue"
						for _, a := range t.Attr {
							if a.Name.Local == "val" {
								cell.Merge = a.Value
							}
						}
					}
				}
			case xml.CharData:
				if inText {
					paragraph.Write(t)
					if paragraph.Len() > 256<<10 {
						reader.Close()
						return ErrLimit
					}
				}
			case xml.EndElement:
				if t.Name.Space != "http://schemas.openxmlformats.org/wordprocessingml/2006/main" {
					continue
				}
				switch t.Name.Local {
				case "t":
					inText = false
				case "p":
					if cell != nil {
						if cell.Text != "" {
							cell.Text += "\n"
						}
						cell.Text += paragraph.String()
					} else if e = emit(Item{Text: paragraph.String(), Heading: heading}); e != nil {
						reader.Close()
						return e
					}
				case "tc":
					if row == nil || cell == nil {
						reader.Close()
						return ErrInvalid
					}
					row.Cells = append(row.Cells, *cell)
					sheet.Columns = max(sheet.Columns, cell.Column+max(1, cell.Span))
					cell = nil
				case "tr":
					if row == nil {
						reader.Close()
						return ErrInvalid
					}
					if len(nested) > 0 {
						parent := nested[len(nested)-1]
						nested = nested[:len(nested)-1]
						for _, c := range row.Cells {
							if parent.cell.Text != "" {
								parent.cell.Text += "\n"
							}
							parent.cell.Text += c.Text
						}
						row, cell = parent.row, parent.cell
						continue
					}
					if e = emit(*row); e != nil {
						reader.Close()
						return e
					}
					row = nil
				}
			}
		}
		reader.Close()
	}
	if !found {
		return ErrInvalid
	}
	manifest.Sheets = []Sheet{sheet}
	manifest.Notices = []string{"layout_simplified", "images_omitted"}
	return nil
}

func Open(directory string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(directory, "model.sqlite")+"?mode=ro&immutable=1")
	if err == nil {
		db.SetMaxOpenConns(4)
	}
	return db, err
}
func ReadManifest(ctx context.Context, db *sql.DB) (Manifest, error) {
	var manifest Manifest
	var raw string
	err := db.QueryRowContext(ctx, `SELECT data FROM metadata`).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &manifest)
	}
	return manifest, err
}
func ReadWindow(ctx context.Context, db *sql.DB, sheet, start, limit int) (Window, error) {
	return ReadTile(ctx, db, sheet, start, limit, -1, 0)
}
func ReadTile(ctx context.Context, db *sql.DB, sheet, start, limit, fromColumn, columns int) (Window, error) {
	window := Window{Start: start, Coverage: "complete", Items: []Item{}}
	if sheet < 0 || start < 0 || limit < 1 || limit > 100 {
		return window, ErrInvalid
	}
	if fromColumn < -1 || fromColumn > 16_383 || (fromColumn >= 0 && (columns < 1 || columns > 64)) {
		return window, ErrInvalid
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE sheet=?`, sheet).Scan(&window.Total); err != nil {
		return window, err
	}
	rows, err := db.QueryContext(ctx, `SELECT data FROM items WHERE sheet=? AND position>=? ORDER BY position LIMIT ?`, sheet, start, limit)
	if err != nil {
		return window, err
	}
	defer rows.Close()
	bytes := 0
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return window, err
		}
		var item Item
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			return window, err
		}
		if fromColumn >= 0 && item.Cells != nil {
			visible := []Cell{}
			for _, cell := range item.Cells {
				if cell.Column >= fromColumn && cell.Column < fromColumn+columns {
					visible = append(visible, cell)
				}
			}
			item.Cells = visible
		}
		encoded, e := json.Marshal(item)
		if e != nil {
			return window, e
		}
		if bytes+len(encoded) > 2<<20 {
			break
		}
		bytes += len(encoded)
		window.Items = append(window.Items, item)
	}
	return window, rows.Err()
}
func Find(ctx context.Context, db *sql.DB, sheet, start int, query string) (int, error) {
	if len(query) < 1 || len(query) > 256 || sheet < 0 || start < 0 {
		return -1, ErrInvalid
	}
	var position int
	err := db.QueryRowContext(ctx, `SELECT position FROM items WHERE sheet=? AND position>=? AND instr(lower(text),lower(?))>0 ORDER BY position LIMIT 1`, sheet, start, query).Scan(&position)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	return position, err
}

func Copy(ctx context.Context, db *sql.DB, sheet, start, end, fromColumn, toColumn int) (string, error) {
	if sheet < 0 || start < 0 || end < start || end-start >= 1000 || fromColumn < 0 || toColumn < fromColumn || toColumn-fromColumn >= 256 {
		return "", ErrInvalid
	}
	rows, err := db.QueryContext(ctx, `SELECT data FROM items WHERE sheet=? AND position BETWEEN ? AND ? ORDER BY position`, sheet, start, end)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var text strings.Builder
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return "", err
		}
		var item Item
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			return "", err
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		if item.Cells == nil {
			text.WriteString(item.Text)
		} else {
			values := map[int]string{}
			for _, cell := range item.Cells {
				values[cell.Column] = cell.Text
			}
			for col := fromColumn; col <= toColumn; col++ {
				if col > fromColumn {
					text.WriteByte('\t')
				}
				value := values[col]
				if strings.ContainsAny(value, "\t\r\n\"") {
					value = "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
				}
				text.WriteString(value)
			}
		}
		if text.Len() > 1<<20 {
			return "", ErrLimit
		}
	}
	return text.String(), rows.Err()
}

func LocateRow(ctx context.Context, db *sql.DB, sheet, row int) (int, error) {
	if sheet < 0 || row < 1 || row > 1_048_576 {
		return -1, ErrInvalid
	}
	var position int
	err := db.QueryRowContext(ctx, `SELECT position FROM items WHERE sheet=? AND row>=? ORDER BY row LIMIT 1`, sheet, row).Scan(&position)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	return position, err
}
