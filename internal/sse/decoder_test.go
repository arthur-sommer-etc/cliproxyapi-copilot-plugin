package sse

import (
	"reflect"
	"testing"
)

func TestDecoderFramesSplitChunks(t *testing.T) {
	t.Parallel()

	var decoder Decoder
	if got := decoder.Feed([]byte("event: one\ndata: {\"a\":")); got != nil {
		t.Fatalf("unexpected incomplete frames: %#v", got)
	}
	got := decoder.Feed([]byte("1}\n\nevent: two\r\ndata: {}\r\n\r\ntrailing"))
	want := [][]byte{
		[]byte("event: one\ndata: {\"a\":1}\n\n"),
		[]byte("event: two\r\ndata: {}\r\n\r\n"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames = %#v, want %#v", got, want)
	}
	if trailing := string(decoder.Flush()); trailing != "trailing" {
		t.Fatalf("trailing data = %q", trailing)
	}
}

func TestDecoderEmptyChunksAndFlushReset(t *testing.T) {
	t.Parallel()
	var decoder Decoder
	decoder.Feed([]byte("data: partial"))
	if frames := decoder.Feed(nil); frames != nil {
		t.Fatalf("empty chunk produced frames: %q", frames)
	}
	if got := string(decoder.Flush()); got != "data: partial" {
		t.Fatalf("empty chunk changed pending data: %q", got)
	}
	if got := decoder.Flush(); got != nil {
		t.Fatalf("flush retained data: %q", got)
	}
	decoder.Feed([]byte(" \t\r\n"))
	if got := decoder.Flush(); got != nil {
		t.Fatalf("whitespace flush = %q", got)
	}
	if got := decoder.Feed([]byte("data: next\n\n")); !reflect.DeepEqual(got, [][]byte{[]byte("data: next\n\n")}) {
		t.Fatalf("whitespace was not cleared: %q", got)
	}
}

func TestDecoderChoosesEarliestMixedSeparator(t *testing.T) {
	t.Parallel()
	for _, first := range []string{"\n\n", "\r\n\r\n"} {
		var decoder Decoder
		second := "\r\n\r\n"
		if first == second {
			second = "\n\n"
		}
		got := decoder.Feed([]byte("data: first" + first + "data: second" + second))
		want := [][]byte{[]byte("data: first" + first), []byte("data: second" + second)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("frames = %q, want %q", got, want)
		}
	}
}
