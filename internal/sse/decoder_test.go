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
		[]byte("event: two\ndata: {}\n\n"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames = %#v, want %#v", got, want)
	}
	if trailing := string(decoder.Flush()); trailing != "trailing" {
		t.Fatalf("trailing data = %q", trailing)
	}
}

func TestDecoderNormalizesLineEndingsAtEverySplit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{name: "LF", input: "event: one\ndata: alpha\n\nevent: two\ndata: beta\n\n"},
		{name: "CRLF", input: "event: one\r\ndata: alpha\r\n\r\nevent: two\r\ndata: beta\r\n\r\n"},
		{name: "CR", input: "event: one\rdata: alpha\r\revent: two\rdata: beta\r\r"},
		{name: "mixed", input: "event: one\r\ndata: alpha\r\r\nevent: two\ndata: beta\r\r"},
	}
	want := [][]byte{
		[]byte("event: one\ndata: alpha\n\n"),
		[]byte("event: two\ndata: beta\n\n"),
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for split := 0; split <= len(test.input); split++ {
				var decoder Decoder
				got := decoder.Feed([]byte(test.input[:split]))
				got = append(got, decoder.Feed([]byte(test.input[split:]))...)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("split %d frames = %#v, want %#v", split, got, want)
				}
			}
			var decoder Decoder
			var got [][]byte
			for index := 0; index < len(test.input); index++ {
				got = append(got, decoder.Feed([]byte{test.input[index]})...)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("byte-at-a-time frames = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDecoderPreservesDataWhitespaceWhileNormalizing(t *testing.T) {
	t.Parallel()

	input := "event: spaced\rdata:  padded value \t\r\r"
	want := [][]byte{[]byte("event: spaced\ndata:  padded value \t\n\n")}
	var decoder Decoder
	var got [][]byte
	for index := 0; index < len(input); index++ {
		got = append(got, decoder.Feed([]byte{input[index]})...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames = %#v, want %#v", got, want)
	}
}

func TestDecoderSkipsSplitCRLFTailAfterEmittingFrame(t *testing.T) {
	t.Parallel()

	var decoder Decoder
	first := decoder.Feed([]byte("event: one\r\ndata: alpha\r\n\r"))
	wantFirst := [][]byte{[]byte("event: one\ndata: alpha\n\n")}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("first frames = %#v, want %#v", first, wantFirst)
	}
	if got := decoder.Feed(nil); got != nil {
		t.Fatalf("empty chunk frames = %#v, want nil", got)
	}
	if got := decoder.Feed([]byte("\n")); got != nil {
		t.Fatalf("CRLF tail frames = %#v, want nil", got)
	}
	second := decoder.Feed([]byte("event: two\r\ndata: beta\r\n\r\n"))
	wantSecond := [][]byte{[]byte("event: two\ndata: beta\n\n")}
	if !reflect.DeepEqual(second, wantSecond) {
		t.Fatalf("second frames = %#v, want %#v", second, wantSecond)
	}
}

func TestDecoderFlushPreservesTrailingEventPolicy(t *testing.T) {
	t.Parallel()

	var decoder Decoder
	if got := decoder.Feed([]byte("data: trailing\r")); got != nil {
		t.Fatalf("trailing incomplete event emitted frames: %#v", got)
	}
	if got := string(decoder.Flush()); got != "data: trailing\n" {
		t.Fatalf("trailing data = %q, want normalized incomplete event", got)
	}
	if got := decoder.Feed([]byte("\ndata: next")); got != nil {
		t.Fatalf("new stream emitted incomplete frames: %#v", got)
	}
	if got := string(decoder.Flush()); got != "\ndata: next" {
		t.Fatalf("new stream trailing data = %q, want leading LF preserved", got)
	}
}
