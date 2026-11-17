package main

import (
	"encoding/json"
	"testing"
)

func TestInputTextShapes(t *testing.T) {
	cases := map[string]string{
		`"hello"`: "hello",
		`[{"type":"text","text":"a"},{"type":"image"}]`: "a[image]",
		`[{"type":"text","text":"no"}]`:                 "no",
	}
	for raw, want := range cases {
		if got := inputText(json.RawMessage(raw)); got != want {
			t.Errorf("inputText(%s) = %q, want %q", raw, got, want)
		}
	}
}
