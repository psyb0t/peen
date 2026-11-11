package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPDFFileMode = 0o600

// buildTestPDF writes a minimal PDF with one Helvetica text line per page.
// An empty string makes a page with no text. The xref offsets are computed
// from the bytes written, as a real PDF needs.
func buildTestPDF(t *testing.T, pageTexts ...string) []byte {
	t.Helper()

	pageCount := len(pageTexts)
	objects := make([]string, 0, 3+2*pageCount)

	kids := make([]string, 0, pageCount)
	for index := range pageTexts {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+2*index))
	}

	objects = append(objects,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>",
			strings.Join(kids, " "), pageCount),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	)

	for index, text := range pageTexts {
		stream := ""
		if text != "" {
			stream = fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		}

		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R "+
				"/MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> "+
				"/Contents %d 0 R >>", 5+2*index),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream",
				len(stream), stream),
		)
	}

	var builder strings.Builder

	builder.WriteString("%PDF-1.4\n")

	offsets := make([]int, 0, len(objects))
	for index, object := range objects {
		offsets = append(offsets, builder.Len())
		fmt.Fprintf(&builder, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}

	xref := builder.Len()
	fmt.Fprintf(&builder, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)

	for _, offset := range offsets {
		fmt.Fprintf(&builder, "%010d 00000 n \n", offset)
	}

	fmt.Fprintf(&builder, "trailer\n<< /Size %d /Root 1 0 R >>\n"+
		"startxref\n%d\n%%%%EOF\n", len(objects)+1, xref)

	return []byte(builder.String())
}

func writeTestPDF(t *testing.T, dir, name string, content []byte) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, content, testPDFFileMode))

	return path
}

func TestReadFileExtractsPDFTextPageByPage(t *testing.T) {
	t.Parallel()
	executor := newTestExecutor(t)
	content := buildTestPDF(t, "First page words", "Second page words")
	path := writeTestPDF(t, executor.workspace, "report.PDF", content)

	output, err := executor.ReadFile(t.Context(), ReadFileInput{Path: path})
	require.NoError(t, err)

	assert.Equal(t, pdfMediaType, output.MediaType)
	assert.Equal(t, 2, output.Pages)
	assert.Equal(t, len(content), output.SizeBytes)
	assert.Equal(t, hashBytes(content), output.SHA256)
	assert.Empty(t, output.Note)
	assert.Nil(t, output.Image)
	assert.Equal(t, "     1\t--- page 1 of 2 ---\n     2\tFirst page words\n"+
		"     3\t--- page 2 of 2 ---\n     4\tSecond page words\n"+
		"[lines 1-4 of 4]", output.Content)
	assert.Equal(t, 4, output.TotalLines)

	window, err := executor.ReadFile(t.Context(), ReadFileInput{
		Path: path, Offset: 3, Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "     3\t--- page 2 of 2 ---\n     4\tSecond page words\n"+
		"[lines 3-4 of 4]", window.Content)
	assert.Equal(t, 3, window.FirstLine)
}

func TestReadFileNotesAPDFWithoutText(t *testing.T) {
	t.Parallel()
	executor := newTestExecutor(t)
	path := writeTestPDF(t, executor.workspace, "scan.pdf",
		buildTestPDF(t, ""))

	output, err := executor.ReadFile(t.Context(), ReadFileInput{Path: path})
	require.NoError(t, err)
	assert.Equal(t, 1, output.Pages)
	assert.Equal(t, pdfNoTextNote, output.Note)
}

func TestReadFileRefusesWhatIsNotAReadablePDF(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		content []byte
		limits  Limits
		wantErr error
	}{
		{
			name:    "not PDF data",
			content: []byte("plain text with a pdf name"),
			wantErr: ErrBinaryContent,
		},
		{
			name:    "malformed",
			content: []byte("%PDF-1.4\nthis is not a real document"),
			wantErr: commerr.ErrParseFailed,
		},
		{
			name:    "over the limit",
			content: []byte("%PDF-1.4\n" + strings.Repeat("x", 64)),
			limits:  Limits{MaxPDFBytes: 16},
			wantErr: ErrLimitExceeded,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			executor := newTestExecutorWithLimits(t, tc.limits)
			path := writeTestPDF(t, executor.workspace, "file.pdf", tc.content)

			_, err := executor.ReadFile(t.Context(), ReadFileInput{Path: path})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
