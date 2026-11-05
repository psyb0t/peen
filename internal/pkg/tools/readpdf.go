package tools

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	pdfExtension = ".pdf"
	pdfMediaType = "application/pdf"
	// pdfPageMarkerFormat starts each page's text, so a line window still
	// says which page it is on.
	pdfPageMarkerFormat = "--- page %d of %d ---"
	pdfPageUnreadable   = "[this page's text could not be read]"
	// pdfLinesPerPage is the parts each page adds: its marker and its text.
	pdfLinesPerPage = 2
	pdfNoTextNote   = "The PDF has no text layer, so it is probably scanned images. Its text cannot be read." //nolint:lll // One model-facing sentence pair.
)

//nolint:gochecknoglobals // Fixed file signature.
var pdfSignature = []byte("%PDF-")

// isPDFPath reports whether readFile reads path as a PDF.
func isPDFPath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), pdfExtension)
}

// readPDFFile reads a PDF whole, bounded by MaxPDFBytes, and returns its text
// as the window input selects, one marker line before each page. The window,
// hash, and observation work as for a text file, over the extracted text, and
// SHA256 is the hash of the file's bytes.
func (e *Executor) readPDFFile(
	resolved string,
	input ReadFileInput,
) (ReadFileOutput, error) {
	content, _, err := e.readRegularFile(resolved,
		int64(e.Limits().MaxPDFBytes))
	if err != nil {
		return ReadFileOutput{}, ctxerrors.Wrap(err, "read PDF file")
	}

	if !bytes.HasPrefix(content, pdfSignature) {
		return ReadFileOutput{}, ctxerrors.Wrapf(ErrBinaryContent,
			"%s is not PDF data", resolved)
	}

	text, pages, err := pdfText(content)
	if err != nil {
		return ReadFileOutput{}, ctxerrors.Wrapf(err, "read PDF %s", resolved)
	}

	sha := hashBytes(content)
	output := e.buildReadWindow(resolved, text, sha, input)
	output.MediaType = pdfMediaType
	output.SizeBytes = len(content)
	output.Pages = pages

	if strings.TrimSpace(pdfWithoutMarkers(text, pages)) == "" {
		output.Note = pdfNoTextNote
	}

	e.observeContent(resolved, sha)

	return output, nil
}

// pdfText extracts every page's text. The library reports a malformed file
// by panicking, so a panic is turned into a parse error.
func pdfText(content []byte) (string, int, error) {
	var (
		text  string
		pages int
		err   error
	)

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				text, pages = "", 0
				err = ctxerrors.Wrapf(commerr.ErrParseFailed,
					"malformed PDF: %v", recovered)
			}
		}()

		text, pages, err = extractPDFText(content)
	}()

	return text, pages, err
}

func extractPDFText(content []byte) (string, int, error) {
	reader, err := pdf.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", 0, ctxerrors.Wrap(
			errors.Join(commerr.ErrParseFailed, err), "open PDF")
	}

	pages := reader.NumPage()
	parts := make([]string, 0, pages*pdfLinesPerPage)

	for number := 1; number <= pages; number++ {
		parts = append(parts, fmt.Sprintf(pdfPageMarkerFormat, number, pages))

		pageText, pageErr := reader.Page(number).GetPlainText(nil)
		if pageErr != nil {
			parts = append(parts, pdfPageUnreadable)

			continue
		}

		parts = append(parts, strings.TrimSpace(pageText))
	}

	return strings.Join(parts, "\n"), pages, nil
}

// pdfWithoutMarkers is text with the page marker lines removed, to tell an
// empty PDF from one that has text.
func pdfWithoutMarkers(text string, pages int) string {
	for number := 1; number <= pages; number++ {
		text = strings.Replace(text,
			fmt.Sprintf(pdfPageMarkerFormat, number, pages), "", 1)
	}

	return text
}
