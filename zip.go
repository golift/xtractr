package xtractr

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/saintfish/chardet"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

/* How to extract a ZIP file. */

// ExtractZIP extracts a zip file.. to a destination. Simple enough.
func ExtractZIP(xFile *XFile) (size uint64, filesList []string, err error) {
	zipReader, err := zip.OpenReader(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("zip.OpenReader: %w", err)
	}
	defer zipReader.Close()

	defer xFile.newProgress(getUncompressedZipSize(zipReader)).done()

	// Detect encoding for non-UTF8 filenames in the archive.
	decoder := detectZipEncoding(xFile, zipReader.File)

	files := []string{}

	for _, zipFile := range zipReader.File {
		decodedName := decodeZipFilename(zipFile.Name, zipFile.NonUTF8, decoder)

		fSize, wfile, err := xFile.unzipWithName(zipFile, decodedName)
		if err != nil {
			return xFile.prog.Wrote, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
		}

		files = append(files, filepath.Join(xFile.OutputDir, decodedName))
		xFile.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			wfile, fSize, xFile.prog.Files, xFile.prog.Wrote)
	}

	files, err = xFile.cleanup(files)

	return xFile.prog.Wrote, files, err
}

// detectZipEncoding scans all zip file entries for non-UTF8 filenames and
// attempts to detect their character encoding. It uses chardet for initial
// candidates, then validates and scores each one. Returns nil if no non-UTF8
// filenames are found or no suitable decoder can be determined.
func detectZipEncoding(xFile *XFile, entries []*zip.File) *encoding.Decoder { //nolint:cyclop,funlen
	var rawNames []string

	for _, f := range entries {
		if f.NonUTF8 || !utf8.ValidString(f.Name) {
			rawNames = append(rawNames, f.Name)
		}
	}

	if len(rawNames) == 0 {
		return nil
	}

	// Concatenate all non-UTF8 names for charset detection.
	var allBytes []byte
	for _, name := range rawNames {
		allBytes = append(allBytes, []byte(name)...)
	}

	detector := chardet.NewTextDetector()

	results, err := detector.DetectAll(allBytes)
	if err != nil {
		xFile.Debugf("Charset detection failed for zip filenames: %v", err)
		return nil
	}

	type candidate struct {
		charset    string
		confidence int
		enc        encoding.Encoding
		score      int
	}

	var candidates []candidate

	for _, result := range results {
		enc := charsetToEncoding(result.Charset)
		if enc == nil {
			continue
		}

		decoder := enc.NewDecoder()

		decoded, valid := decodeAll(decoder, rawNames)
		if !valid {
			continue
		}

		// Score: chardet confidence + script analysis + encoding preference.
		score := result.Confidence
		score += scriptConsistencyScore(decoded)
		score += encodingPreference(result.Charset)

		candidates = append(candidates, candidate{
			charset:    result.Charset,
			confidence: result.Confidence,
			enc:        enc,
			score:      score,
		})
	}

	if len(candidates) == 0 {
		xFile.Debugf("No suitable encoding found for %d non-UTF8 zip filenames", len(rawNames))
		return nil
	}

	// Pick the candidate with the highest combined score.
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}

	xFile.Debugf("Detected zip filename encoding: %s (confidence: %d, score: %d)",
		best.charset, best.confidence, best.score)

	return best.enc.NewDecoder()
}

// encodingPreference returns a tiebreaker score based on how commonly an
// encoding is encountered in non-UTF8 zip archives in the real world.
// This is used when chardet gives equal confidence to multiple encodings
// (which is common with short filename samples).
//
// GBK/GB-18030 is most common because Chinese Windows is the largest source
// of non-UTF8 zip files. Shift-JIS is next (Japanese Windows). These are
// preferred over less common encodings when all other signals are equal.
// Encoding preference scores for tiebreaking. Higher = more commonly seen in non-UTF8 zips.
const (
	prefGBK     = 5 // Chinese Windows is the largest source of non-UTF8 zip files
	prefShiftJS = 4 // Japanese Windows
	prefBig5    = 3 // Traditional Chinese
	prefEUCKR   = 2 // Korean
	prefEUCJP   = 1 // Japanese (less common than Shift-JIS in zips)
)

func encodingPreference(charset string) int {
	switch strings.ToLower(charset) {
	case "gb-2312", "gb2312", "gbk", "gb18030", "gb-18030":
		return prefGBK
	case "shift_jis", "shift-jis":
		return prefShiftJS
	case "big5", "big-5":
		return prefBig5
	case "euc-kr":
		return prefEUCKR
	case "euc-jp":
		return prefEUCJP
	default:
		return 0
	}
}

// decodeAll attempts to decode every name using the given decoder.
// Returns the decoded names and whether all decoded successfully into valid UTF-8.
func decodeAll(decoder *encoding.Decoder, names []string) ([]string, bool) {
	decoded := make([]string, len(names))

	for idx, name := range names {
		d, err := decoder.String(name)
		if err != nil || !utf8.ValidString(d) {
			return nil, false
		}

		decoded[idx] = d
	}

	return decoded, true
}

// Script scoring constants for disambiguating CJK encodings.
const (
	scoreKanaBonus      = 200 // kana is an unambiguous marker for Japanese
	scoreHangulBonus    = 150 // pure Hangul is a clear marker for Korean
	scoreMixedHangulCJK = 50  // mixed Hangul + CJK is suspicious (likely wrong encoding)
	scorePureCJK        = 100 // pure CJK Unified: consistent Chinese text
	scorePercentDivisor = 100 // used to compute percentage-based scores
	maxASCII            = 0x80
)

// scriptConsistencyScore examines decoded filenames and returns a score based
// on how consistent the non-ASCII characters are within known Unicode blocks.
// Higher scores indicate text that looks like a natural writing system rather
// than garbled characters from a wrong encoding.
func scriptConsistencyScore(decoded []string) int { //nolint:cyclop
	counts := countScriptRunes(decoded)

	total := counts.cjk + counts.hiragana + counts.katakana + counts.hangul + counts.latin + counts.other
	if total == 0 {
		return 0
	}

	kana := counts.hiragana + counts.katakana

	// Kana characters are unambiguous markers for Japanese text.
	// When decoded text contains kana, that encoding is almost certainly correct.
	if kana > 0 {
		return scoreKanaBonus + kana*scorePercentDivisor/total
	}

	// Pure Hangul (no CJK mix) is a clear marker for Korean.
	if counts.hangul > 0 && counts.cjk == 0 {
		return scoreHangulBonus + counts.hangul*scorePercentDivisor/total
	}

	// Mixed Hangul + CJK is suspicious - usually means wrong encoding
	// (e.g., GBK bytes decoded as EUC-KR).
	if counts.hangul > 0 && counts.cjk > 0 {
		return scoreMixedHangulCJK
	}

	// Pure CJK Unified: consistent Chinese text (GBK/Big5).
	if counts.cjk > 0 && counts.other == 0 && counts.latin == 0 {
		return scorePureCJK
	}

	// General consistency score.
	dominant := max(counts.latin, max(counts.hangul, max(kana, counts.cjk)))

	return dominant * scorePercentDivisor / total
}

// scriptCounts holds per-script character counts from decoded filenames.
type scriptCounts struct {
	cjk      int // CJK Unified Ideographs (U+4E00-U+9FFF)
	hiragana int // Japanese Hiragana (U+3040-U+309F)
	katakana int // Japanese Katakana (U+30A0-U+30FF)
	hangul   int // Korean Hangul Syllables (U+AC00-U+D7AF)
	latin    int // Latin Extended (U+00C0-U+024F)
	other    int // everything else non-ASCII
}

// countScriptRunes classifies non-ASCII runes in the decoded names by Unicode block.
func countScriptRunes(decoded []string) scriptCounts { //nolint:cyclop
	var counts scriptCounts

	for _, name := range decoded {
		for _, char := range name {
			switch {
			case char < maxASCII:
				// ASCII - ignore for scoring
			case char >= 0x4E00 && char <= 0x9FFF:
				counts.cjk++
			case char >= 0x3040 && char <= 0x309F:
				counts.hiragana++
			case char >= 0x30A0 && char <= 0x30FF:
				counts.katakana++
			case char >= 0xAC00 && char <= 0xD7AF:
				counts.hangul++
			case char >= 0x00C0 && char <= 0x024F:
				counts.latin++
			default:
				counts.other++
			}
		}
	}

	return counts
}

// decodeZipFilename decodes a zip entry filename if it's non-UTF8 and a decoder is available.
func decodeZipFilename(name string, nonUTF8 bool, decoder *encoding.Decoder) string {
	if decoder == nil {
		return name
	}

	if !nonUTF8 && utf8.ValidString(name) {
		return name
	}

	decoded, err := decoder.String(name)
	if err != nil {
		return name // fall back to original on error
	}

	return decoded
}

// charsetToEncoding maps charset names returned by chardet to golang.org/x/text encodings.
func charsetToEncoding(charset string) encoding.Encoding { //nolint:cyclop,funlen,ireturn
	switch strings.ToLower(charset) {
	case "gb-2312", "gb2312", "gbk", "gb18030", "gb-18030":
		return simplifiedchinese.GBK
	case "big5", "big-5":
		return traditionalchinese.Big5
	case "euc-jp":
		return japanese.EUCJP
	case "shift_jis", "shift-jis":
		return japanese.ShiftJIS
	case "iso-2022-jp":
		return japanese.ISO2022JP
	case "euc-kr":
		return korean.EUCKR
	case "iso-8859-1", "windows-1252":
		return charmap.Windows1252
	case "iso-8859-2":
		return charmap.ISO8859_2
	case "iso-8859-5":
		return charmap.ISO8859_5
	case "iso-8859-6":
		return charmap.ISO8859_6
	case "iso-8859-7":
		return charmap.ISO8859_7
	case "iso-8859-8":
		return charmap.ISO8859_8
	case "iso-8859-9":
		return charmap.ISO8859_9
	case "windows-1250":
		return charmap.Windows1250
	case "windows-1251":
		return charmap.Windows1251
	case "windows-1253":
		return charmap.Windows1253
	case "windows-1255":
		return charmap.Windows1255
	case "windows-1256":
		return charmap.Windows1256
	case "koi8-r":
		return charmap.KOI8R
	default:
		return nil
	}
}

func getUncompressedZipSize(zipReader *zip.ReadCloser) (total, compressed uint64, count int) {
	for _, zipFile := range zipReader.File {
		total += zipFile.UncompressedSize64
		// compressed += zipFile.CompressedSize64
		count++
	}

	return total, 0, count
}

func (x *XFile) unzipWithName(zipFile *zip.File, name string) (uint64, string, error) {
	zFile, err := zipFile.Open()
	if err != nil {
		return 0, name, fmt.Errorf("zipFile.Open: %w", err)
	}
	defer zFile.Close()

	file := &file{
		Path:     x.clean(name),
		Data:     zFile,
		FileMode: zipFile.Mode(),
		DirMode:  x.DirMode,
		Mtime:    zipFile.Modified,
		Atime:    time.Now(),
	}

	if !strings.HasPrefix(file.Path, x.OutputDir) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		err := fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), ErrInvalidPath, file.Path, name)
		return 0, file.Path, err
	}

	if zipFile.FileInfo().IsDir() {
		x.Debugf("Writing archived directory: %s", file.Path)

		err := x.mkDir(file.Path, zipFile.Mode(), zipFile.Modified)
		if err != nil {
			return 0, file.Path, fmt.Errorf("making zipFile dir: %w", err)
		}

		return 0, file.Path, nil
	}

	x.Debugf("Writing archived file: %s (packed: %d, unpacked: %d)", file.Path,
		zipFile.CompressedSize64, zipFile.UncompressedSize64)

	s, err := x.write(file)
	if err != nil {
		return s, file.Path, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), err, file.Path, name)
	}

	return s, file.Path, nil
}
