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
