package xtractr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
)

//nolint:gosmopolitan
func TestDecodeZipFilename_PrefersPartEncodingForSharedFolders(t *testing.T) {
	t.Parallel()

	gbkEncoder := simplifiedchinese.GBK.NewEncoder()
	shiftJISEncoder := japanese.ShiftJIS.NewEncoder()

	rootUTF := "感谢你的帮助"
	japaneseLeafUTF := "テスト.txt"
	chineseLeafUTF := "报告.txt"

	rootRaw, err := gbkEncoder.String(rootUTF)
	require.NoError(t, err)

	japaneseLeafRaw, err := shiftJISEncoder.String(japaneseLeafUTF)
	require.NoError(t, err)

	chineseLeafRaw, err := gbkEncoder.String(chineseLeafUTF)
	require.NoError(t, err)

	// Simulate mixed archive detection where one full path leans Shift-JIS.
	japanesePathRaw := rootRaw + "/" + japaneseLeafRaw
	chinesePathRaw := rootRaw + "/" + chineseLeafRaw

	decoders := &zipNameDecoders{
		defaultEncoding: simplifiedchinese.GBK,
		nameEncodings: map[string]encoding.Encoding{
			japanesePathRaw: japanese.ShiftJIS, // would garble root without part-level override
			chinesePathRaw:  simplifiedchinese.GBK,
		},
		partNames: map[string]string{
			rootRaw:         rootUTF,
			japaneseLeafRaw: japaneseLeafUTF,
			chineseLeafRaw:  chineseLeafUTF,
		},
	}

	assert.Equal(t, rootUTF+"/"+japaneseLeafUTF, decodeZipFilename(japanesePathRaw, nil, true, decoders))
	assert.Equal(t, rootUTF+"/"+chineseLeafUTF, decodeZipFilename(chinesePathRaw, nil, true, decoders))
}
