// Regenerate only the disposable Excel preview and authoritative source JSON.
// Run from the repository root; never overwrites the original workbook.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"openfox/internal/office"
)

func main() {
	const source = "tests/frontend/e2e/fixtures/office-preview/complex.xlsx"
	raw, err := os.ReadFile(source)
	if err != nil {
		panic(err)
	}
	p, err := office.Read(raw, office.XLSX)
	if err != nil {
		panic(err)
	}
	prepared, err := p.SpreadsheetPreview()
	if err != nil {
		panic(err)
	}
	projection, err := json.MarshalIndent(struct {
		office.View
		SHA256 string `json:"sha256"`
	}{p.View(), fmt.Sprintf("%x", sha256.Sum256(raw))}, "", "  ")
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(source+".render.xlsx", prepared, 0644); err != nil {
		panic(err)
	}
	if err = os.WriteFile(source+".json", append(projection, '\n'), 0644); err != nil {
		panic(err)
	}
	fmt.Printf("Prepared %d bytes, %d sheets; source SHA256 %x\n", len(prepared), len(p.View().Spreadsheet.Sheets), sha256.Sum256(raw))
}
