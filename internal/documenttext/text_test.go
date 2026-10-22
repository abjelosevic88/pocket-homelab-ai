package documenttext

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestAskDocumentText(t *testing.T) {
	archive := func(files map[string]string) []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for name, text := range files {
			f, e := z.Create(name)
			if e != nil {
				t.Fatal(e)
			}
			f.Write([]byte(text))
		}
		z.Close()
		return b.Bytes()
	}
	doc := archive(map[string]string{"word/document.xml": `<w:document xmlns:w="word"><w:p><w:r><w:t>Hello &amp; goodbye</w:t></w:r></w:p></w:document>`})
	text, err := Text("test.docx", doc)
	if err != nil || !strings.Contains(text, "Hello & goodbye") {
		t.Fatal(text, err)
	}
	sheet := archive(map[string]string{"xl/sharedStrings.xml": `<sst><si><t>Revenue</t></si></sst>`, "xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c r="A1" t="s"><v>0</v></c><c r="B1"><v>125</v></c></row></sheetData></worksheet>`})
	text, err = Text("test.xlsx", sheet)
	if err != nil || !strings.Contains(text, "A1: Revenue") || !strings.Contains(text, "B1: 125") {
		t.Fatal(text, err)
	}
	if _, err = Text("malformed.docx", []byte("not a zip")); err == nil {
		t.Fatal("accepted malformed file")
	}
	if _, err = Text("macro.docm", doc); err == nil {
		t.Fatal("accepted unsupported macro format")
	}
	if _, err = Text("binary.txt", []byte{0, 1, 2}); err == nil {
		t.Fatal("accepted binary text")
	}
	if _, err = Text("bomb.docx", archive(map[string]string{"word/document.xml": strings.Repeat("x", (8<<20)+1)})); err == nil {
		t.Fatal("accepted oversized expanded entry")
	}
}
