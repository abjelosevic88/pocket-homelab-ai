package documenttext

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Text extracts bounded textual content without launching macros, fetching
// external references, or evaluating spreadsheet formulas.
func Text(name string, data []byte) (string, error) {
	if len(data) > 20<<20 {
		return "", errors.New("document is too large for text extraction (20 MiB maximum)")
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".txt", ".md", ".csv", ".tsv", ".json", ".xml":
		if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return "", errors.New("file is not UTF-8 text")
		}
		return boundedText(string(data)), nil
	case ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp":
	default:
		return "", errors.New("text extraction supports DOCX, XLSX, PPTX, OpenDocument and UTF-8 text; export older or binary formats to one of these first")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("document archive could not be read")
	}
	if len(z.File) > 1000 {
		return "", errors.New("document contains too many archive entries")
	}
	var files []*zip.File
	var shared []string
	expanded := uint64(0)
	read := func(f *zip.File) ([]byte, error) {
		expanded += f.UncompressedSize64
		if f.UncompressedSize64 > 8<<20 || expanded > 32<<20 {
			return nil, errors.New("document section is too large")
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		defer r.Close()
		b, e := io.ReadAll(io.LimitReader(r, (8<<20)+1))
		if len(b) > 8<<20 {
			return nil, errors.New("document section is too large")
		}
		return b, e
	}
	for _, f := range z.File {
		n := f.Name
		if n == "xl/sharedStrings.xml" {
			b, e := read(f)
			if e != nil {
				return "", e
			}
			var stringsXML struct {
				Items []struct {
					Text string `xml:"t"`
					Runs []struct {
						Text string `xml:"t"`
					} `xml:"r"`
				} `xml:"si"`
			}
			if xml.Unmarshal(b, &stringsXML) != nil {
				return "", errors.New("invalid spreadsheet strings")
			}
			for _, it := range stringsXML.Items {
				v := it.Text
				for _, r := range it.Runs {
					v += r.Text
				}
				shared = append(shared, v)
			}
		}
		if n == "word/document.xml" || n == "content.xml" || (strings.HasPrefix(n, "ppt/slides/slide") && strings.HasSuffix(n, ".xml")) || (strings.HasPrefix(n, "xl/worksheets/sheet") && strings.HasSuffix(n, ".xml")) {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	var out strings.Builder
	for _, f := range files {
		if out.Len() > 180000 {
			break
		}
		b, e := read(f)
		if e != nil {
			return "", e
		}
		out.WriteString("\n[" + f.Name + "]\n")
		if strings.HasPrefix(f.Name, "xl/worksheets/") {
			var sheet struct {
				Rows []struct {
					Cells []struct {
						Ref    string `xml:"r,attr"`
						Type   string `xml:"t,attr"`
						Value  string `xml:"v"`
						Inline string `xml:"is>t"`
					} `xml:"c"`
				} `xml:"sheetData>row"`
			}
			if xml.Unmarshal(b, &sheet) != nil {
				return "", errors.New("invalid spreadsheet")
			}
			for _, row := range sheet.Rows {
				for _, c := range row.Cells {
					v := c.Value
					if c.Type == "s" {
						i, e := strconv.Atoi(v)
						if e == nil && i >= 0 && i < len(shared) {
							v = shared[i]
						}
					}
					if c.Type == "inlineStr" {
						v = c.Inline
					}
					out.WriteString(c.Ref + ": " + v + "\t")
				}
				out.WriteByte('\n')
				if out.Len() > 180000 {
					break
				}
			}
		} else {
			d := xml.NewDecoder(bytes.NewReader(b))
			capture := 0
			for out.Len() <= 180000 {
				tok, e := d.Token()
				if e == io.EOF {
					break
				}
				if e != nil {
					return "", errors.New("invalid document XML")
				}
				switch x := tok.(type) {
				case xml.StartElement:
					if x.Name.Local == "t" || x.Name.Local == "p" || x.Name.Local == "h" {
						capture++
					}
				case xml.EndElement:
					if x.Name.Local == "t" || x.Name.Local == "p" || x.Name.Local == "h" {
						capture--
					}
					if x.Name.Local == "p" || x.Name.Local == "h" || x.Name.Local == "tr" {
						out.WriteByte('\n')
					}
				case xml.CharData:
					if capture > 0 {
						out.Write(x)
					}
				}
			}
		}
	}
	return boundedText(out.String()), nil
}
func boundedText(s string) string {
	if len(s) > 180000 {
		return strings.ToValidUTF8(s[:180000], "") + "\n[Text truncated]"
	}
	return s
}
