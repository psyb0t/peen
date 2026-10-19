// Package contentblob stores the repeated parts of a JSON document once, by
// content, and replaces them in the document with short references.
//
// A turn's durable state repeats itself: every saved step carries the system
// prompt, the tool descriptions and the whole message history so far. Kept
// inline, that state grows with the square of the turn's rounds. Compacted,
// each long string and each run of array elements is stored once, and every
// later copy is a 64-character hash, so the state grows only by what is new.
//
// Array runs are cut where an element's own hash says so, never by position,
// so an array that only gained elements at its end yields the same runs as
// before plus a new one. That is what keeps a growing message history cheap.
//
// Expand(Compact(x)) returns x byte for byte. Callers compare stored JSON with
// freshly encoded JSON, so a codec that reordered keys or re-escaped text would
// break them. A reference is replaced by the exact bytes it names, and those
// bytes may hold references of their own.
package contentblob

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	// MinBlobBytes is the shortest string literal or array run, in encoded
	// bytes, that is moved out of a document. A reference costs about 90
	// bytes, so shorter parts stay inline.
	MinBlobBytes = 512

	// runBoundaryModulus sets the average array run length: a run ends after
	// an element whose hash's first byte is a multiple of it. Eight references
	// make a run long enough to be moved out again one level up.
	runBoundaryModulus = 8

	// maxRunLevels bounds how many times the runs of an array are folded into
	// runs of runs. Each level divides the references by about eight.
	maxRunLevels = 16

	// maxRunElements ends a run that found no boundary. The count starts at
	// the previous boundary, so it is as stable as the boundaries are.
	maxRunElements = 64

	// maxExpandDepth bounds how many layers of references Expand follows.
	// Real documents nest runs inside strings inside runs a few times at most.
	maxExpandDepth = 32

	hashLength = sha256.Size * 2
)

// The markers use uppercase hex in their \u escapes. Go's JSON encoder writes
// lowercase hex, so no document Peen encodes contains them. A document that
// contains the marker text anyway is stored uncompacted, which keeps Expand
// from ever mistaking data for a reference.
const (
	escapedOpen    = `\` + `u003C`
	escapedClose   = `\` + `u003E`
	markerText     = escapedOpen + `peen-blob`
	envelopePrefix = `{"` + escapedOpen + `peen-blobs` + escapedClose + `":`
	envelopeSuffix = `}`
	referenceStart = `{"` + escapedOpen + `peen-blob` + escapedClose + `":"`
	referenceEnd   = `"}`

	// elementRunWrapper is what ValidBlob puts around a blob to check that
	// it is one value or a run of array elements.
	elementRunWrapper = `[]`
)

var errNotJSON = ctxerrors.Wrap(commerr.ErrValidationFailed, "not JSON")

// Blob is one stored part of a document, the exact bytes a reference stands
// for, and the hex SHA-256 of those bytes. The bytes are either one value or a
// comma-separated run of array elements.
type Blob struct {
	Hash string
	Data []byte
}

// FetchFunc returns the stored data for each requested hash. A hash it cannot
// find is left out of the map.
type FetchFunc func(hashes []string) (map[string][]byte, error)

// Compact moves every long string value and every long run of array elements
// of raw into blobs and returns the compacted document. It returns raw
// unchanged, with no blobs, when raw is short, is not JSON, has nothing long
// enough to move, or already contains the marker text.
func Compact(raw []byte) ([]byte, []Blob) {
	// Invalid JSON would produce blobs ValidBlob rejects on the way back in,
	// so it stays whole. Writes are rare next to reads, so this check is
	// cheap where it runs.
	if len(raw) < MinBlobBytes || bytes.Contains(raw, []byte(markerText)) ||
		!json.Valid(raw) {
		return raw, nil
	}

	compactor := &compactor{raw: raw, seen: map[string]bool{}}

	start := skipSpace(raw, 0)

	body, end, err := compactor.value(start)
	if err != nil || skipSpace(raw, end) != len(raw) {
		return raw, nil
	}

	if len(compactor.blobs) == 0 {
		return raw, nil
	}

	compacted := make([]byte, 0,
		len(envelopePrefix)+len(body)+len(raw)-end+start+len(envelopeSuffix))
	compacted = append(compacted, envelopePrefix...)
	compacted = append(compacted, raw[:start]...)
	compacted = append(compacted, body...)
	compacted = append(compacted, raw[end:]...)
	compacted = append(compacted, envelopeSuffix...)

	return compacted, compactor.blobs
}

// IsCompacted reports whether raw is a document Compact rewrote.
func IsCompacted(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte(envelopePrefix)) &&
		bytes.HasSuffix(raw, []byte(envelopeSuffix))
}

// References returns the distinct hashes raw refers to directly, in first-use
// order. The blobs they name may refer to more.
func References(raw []byte) []string {
	if !IsCompacted(raw) {
		return nil
	}

	return referencesIn(envelopeBody(raw))
}

// Expand restores a document Compact rewrote, fetching every referenced blob,
// layer by layer. A document Compact left alone is returned unchanged. A
// missing blob is an error. fetch must return only blobs whose bytes match
// their hash; CachedFetch checks each blob once, when it is first loaded.
func Expand(raw []byte, fetch FetchFunc) ([]byte, error) {
	if !IsCompacted(raw) {
		return raw, nil
	}

	body := envelopeBody(raw)
	if len(referencesIn(body)) == 0 {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compacted document holds no blob references")
	}

	for range maxExpandDepth {
		hashes := referencesIn(body)
		if len(hashes) == 0 {
			return body, nil
		}

		blobs, err := fetch(hashes)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "fetch content blobs")
		}

		for _, hash := range hashes {
			if _, found := blobs[hash]; !found {
				return nil, ctxerrors.Wrapf(commerr.ErrNotFound,
					"content blob %s", hash)
			}
		}

		body = splice(body, blobs)
	}

	return nil, ctxerrors.Wrapf(commerr.ErrValidationFailed,
		"compacted document nests references deeper than %d", maxExpandDepth)
}

// ValidBlob reports whether data is a part Compact could have stored under
// hash: bytes matching the hash that form one value or a run of array
// elements.
func ValidBlob(hash string, data []byte) bool {
	if len(data) == 0 || hashOf(data) != hash {
		return false
	}

	wrapped := make([]byte, 0, len(data)+len(elementRunWrapper))
	wrapped = append(wrapped, elementRunWrapper[0])
	wrapped = append(wrapped, data...)
	wrapped = append(wrapped, elementRunWrapper[1])

	return json.Valid(wrapped)
}

type compactor struct {
	raw   []byte
	blobs []Blob
	seen  map[string]bool
}

// value compacts the value that starts at start and returns its compacted
// bytes and the index just past it in raw.
func (c *compactor) value(start int) ([]byte, int, error) {
	if start >= len(c.raw) {
		return nil, 0, errNotJSON
	}

	switch c.raw[start] {
	case '"':
		end, ok := stringLiteralEnd(c.raw, start)
		if !ok {
			return nil, 0, errNotJSON
		}

		literal := c.raw[start : end+1]
		if len(literal) >= MinBlobBytes {
			return c.reference(literal), end + 1, nil
		}

		return literal, end + 1, nil
	case '{':
		return c.object(start)
	case '[':
		return c.array(start)
	default:
		end := primitiveEnd(c.raw, start)
		if end == start {
			return nil, 0, errNotJSON
		}

		return c.raw[start:end], end, nil
	}
}

// object copies every byte of an object except its member values, which it
// compacts. Keys always stay inline.
func (c *compactor) object(start int) ([]byte, int, error) {
	var out bytes.Buffer

	out.WriteByte('{')

	index := start + 1
	last := index

	index = skipSpace(c.raw, index)
	if index < len(c.raw) && c.raw[index] == '}' {
		out.Write(c.raw[last : index+1])

		return out.Bytes(), index + 1, nil
	}

	for {
		valueStart, err := c.memberValue(index)
		if err != nil {
			return nil, 0, err
		}

		out.Write(c.raw[last:valueStart])

		compacted, end, err := c.value(valueStart)
		if err != nil {
			return nil, 0, err
		}

		out.Write(compacted)

		last = end

		next, closed, err := c.separator(end, '}')
		if err != nil {
			return nil, 0, err
		}

		if closed {
			out.Write(c.raw[last : next+1])

			return out.Bytes(), next + 1, nil
		}

		index = next
	}
}

// memberValue reads the key and colon of the member that starts at index and
// returns where its value starts.
func (c *compactor) memberValue(index int) (int, error) {
	if index >= len(c.raw) || c.raw[index] != '"' {
		return 0, errNotJSON
	}

	keyEnd, ok := stringLiteralEnd(c.raw, index)
	if !ok {
		return 0, errNotJSON
	}

	index = skipSpace(c.raw, keyEnd+1)
	if index >= len(c.raw) || c.raw[index] != ':' {
		return 0, errNotJSON
	}

	return skipSpace(c.raw, index+1), nil
}

// separator reads what follows a value that ended at end: a comma, after which
// it returns where the next value or key starts, or the closing byte, whose
// index it returns with closed set.
func (c *compactor) separator(end int, closing byte) (int, bool, error) {
	index := skipSpace(c.raw, end)
	if index >= len(c.raw) {
		return 0, false, errNotJSON
	}

	switch c.raw[index] {
	case ',':
		return skipSpace(c.raw, index+1), false, nil
	case closing:
		return index, true, nil
	default:
		return 0, false, errNotJSON
	}
}

type arrayElement struct {
	gap       []byte
	compacted []byte
}

// array compacts each element, then moves long runs of elements out, then
// runs of those references, level by level, like a tree. A run ends after an
// element whose hash says so, so every run but the last stays identical when
// elements are appended, and so does every level above it.
func (c *compactor) array(start int) ([]byte, int, error) {
	elements, closing, end, err := c.arrayElements(start)
	if err != nil {
		return nil, 0, err
	}

	for range maxRunLevels {
		folded, changed := c.foldRuns(elements)
		if !changed {
			break
		}

		elements = folded
	}

	var out bytes.Buffer

	out.WriteByte('[')

	for _, element := range elements {
		out.Write(element.gap)
		out.Write(element.compacted)
	}

	out.Write(closing)

	return out.Bytes(), end, nil
}

// foldRuns replaces each closed run long enough to store with one reference
// element. The open run at the end stays as it is, because a later append
// may still extend it.
func (c *compactor) foldRuns(elements []arrayElement) ([]arrayElement, bool) {
	folded := make([]arrayElement, 0, len(elements))
	changed := false
	runStart := 0

	for index, element := range elements {
		if !runBoundary(element.compacted, index-runStart+1) {
			continue
		}

		run := elements[runStart : index+1]
		runStart = index + 1

		span := runSpan(run)
		if len(span) < MinBlobBytes {
			folded = append(folded, run...)

			continue
		}

		folded = append(folded, arrayElement{
			gap: run[0].gap, compacted: c.reference(span),
		})
		changed = true
	}

	folded = append(folded, elements[runStart:]...)

	return folded, changed
}

func (c *compactor) arrayElements(start int) (
	[]arrayElement, []byte, int, error,
) {
	var elements []arrayElement

	index := start + 1
	last := index

	index = skipSpace(c.raw, index)
	if index < len(c.raw) && c.raw[index] == ']' {
		return nil, c.raw[last : index+1], index + 1, nil
	}

	for {
		compacted, end, err := c.value(index)
		if err != nil {
			return nil, nil, 0, err
		}

		elements = append(elements, arrayElement{
			gap: c.raw[last:index], compacted: compacted,
		})

		last = end

		next, closed, err := c.separator(end, ']')
		if err != nil {
			return nil, nil, 0, err
		}

		if closed {
			return elements, c.raw[last : next+1], next + 1, nil
		}

		index = next
	}
}

// runSpan returns a run's elements and the separators between them. The run's
// first gap, the bytes before its first element, stays outside, so a stored
// run holds only elements and what separates them.
func runSpan(run []arrayElement) []byte {
	var span bytes.Buffer

	for index, element := range run {
		if index > 0 {
			span.Write(element.gap)
		}

		span.Write(element.compacted)
	}

	return span.Bytes()
}

// reference records data as a blob and returns the reference to it.
func (c *compactor) reference(data []byte) []byte {
	hash := hashOf(data)

	if !c.seen[hash] {
		c.seen[hash] = true
		c.blobs = append(c.blobs, Blob{Hash: hash, Data: bytes.Clone(data)})
	}

	reference := make([]byte, 0,
		len(referenceStart)+hashLength+len(referenceEnd))
	reference = append(reference, referenceStart...)
	reference = append(reference, hash...)
	reference = append(reference, referenceEnd...)

	return reference
}

// runBoundary reports whether a run ends after this element: when the
// element's hash says so, or when the run has reached maxRunElements.
func runBoundary(element []byte, length int) bool {
	if length >= maxRunElements {
		return true
	}

	sum := sha256.Sum256(element)

	return sum[0]%runBoundaryModulus == 0
}

func envelopeBody(raw []byte) []byte {
	return raw[len(envelopePrefix) : len(raw)-len(envelopeSuffix)]
}

func referencesIn(body []byte) []string {
	var (
		hashes []string
		seen   = map[string]bool{}
	)

	for offset := 0; ; {
		hash, _, next, ok := nextReference(body, offset)
		if !ok {
			return hashes
		}

		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}

		offset = next
	}
}

// splice replaces every reference in body with the bytes it names.
func splice(body []byte, blobs map[string][]byte) []byte {
	var out bytes.Buffer

	out.Grow(len(body))

	last := 0

	for offset := 0; ; {
		hash, start, next, ok := nextReference(body, offset)
		if !ok {
			break
		}

		out.Write(body[last:start])
		out.Write(blobs[hash])

		last = next
		offset = next
	}

	out.Write(body[last:])

	return out.Bytes()
}

// nextReference finds the first reference at or after offset and returns its
// hash, where it starts, and the offset just past it.
func nextReference(body []byte, offset int) (string, int, int, bool) {
	for {
		position := bytes.Index(body[offset:], []byte(referenceStart))
		if position < 0 {
			return "", 0, 0, false
		}

		start := offset + position
		hashStart := start + len(referenceStart)
		hashEnd := hashStart + hashLength

		if hashEnd+len(referenceEnd) <= len(body) &&
			string(body[hashEnd:hashEnd+len(referenceEnd)]) == referenceEnd {
			return string(body[hashStart:hashEnd]), start,
				hashEnd + len(referenceEnd), true
		}

		offset = start + 1
	}
}

// stringLiteralEnd returns the index of the closing quote of the string
// literal that opens at start.
func stringLiteralEnd(raw []byte, start int) (int, bool) {
	for index := start + 1; index < len(raw); index++ {
		switch raw[index] {
		case '\\':
			index++
		case '"':
			return index, true
		}
	}

	return 0, false
}

// primitiveEnd returns the index just past a number, true, false or null.
// Their exact form is left to json.Valid in ValidBlob and to the decoder.
func primitiveEnd(raw []byte, start int) int {
	index := start
	for index < len(raw) {
		switch raw[index] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return index
		}

		index++
	}

	return index
}

func skipSpace(raw []byte, index int) int {
	for index < len(raw) {
		switch raw[index] {
		case ' ', '\t', '\n', '\r':
			index++
		default:
			return index
		}
	}

	return index
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}
