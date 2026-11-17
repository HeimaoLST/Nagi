package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContactHistoryPreviewKeepsUnicodeMatchAndBounds(t *testing.T) {
	text := strings.Repeat("前文İ", 120) + "预算β结论" + strings.Repeat("后文", 200)
	preview := contactHistoryPreview(text, "预算β")
	if !strings.Contains(preview, "预算β结论") || !utf8.ValidString(preview) || utf8.RuneCountInString(preview) > 242 {
		t.Fatal("search preview lost the match, split UTF-8 or kept the entire long message")
	}
	if !strings.HasPrefix(preview, "…") || !strings.HasSuffix(preview, "…") {
		t.Fatal("the shortened preview did not indicate omitted text")
	}
}
