package main

import (
	"errors"
	"testing"
)

func TestUnsupportedHostPayloadFinalizationRecognizesLegacyHostError(t *testing.T) {
	if !isUnsupportedHostPayloadFinalization(errors.New("unsupported host callback host.payload.finalize")) {
		t.Fatal("legacy host callback error was not classified as unavailable")
	}
	for _, message := range []string{
		"unsupported host callback host.http.do",
		"host payload finalization is unavailable",
		"wrapped: unsupported host callback host.payload.finalize",
	} {
		if isUnsupportedHostPayloadFinalization(errors.New(message)) {
			t.Errorf("error %q was classified as a missing legacy callback", message)
		}
	}
}
