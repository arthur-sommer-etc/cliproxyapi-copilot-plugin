package main

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestHostPayloadFinalizeResponseRejectsMissingBody(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing", body: `{}`},
		{name: "null", body: `{"body":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response hostPayloadFinalizeResponse
			if err := json.Unmarshal([]byte(test.body), &response); err == nil {
				t.Fatal("payload finalization response without a body was accepted")
			}
		})
	}
}

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
