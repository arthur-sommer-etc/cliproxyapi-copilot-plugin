package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCanonicalJSONDigestPreservesExactNumericIdentity(t *testing.T) {
	for _, group := range [][]string{
		{`{"n":1}`, `{"n":1.0}`, `{"n":1e0}`, `{"n":10e-1}`},
		{`{"n":9007199254740993}`, `{"n":9007199254740993.0}`, `{"n":90071992547409930e-1}`},
		{`[0,-0,0.0,-0e9]`, `[0,0,0,0]`},
		{`{"a":0.00100,"z":[3e2]}`, `{"z":[300],"a":1e-3}`},
	} {
		want := canonicalJSONDigest(group[0])
		for _, input := range group[1:] {
			if got := canonicalJSONDigest(input); got != want {
				t.Errorf("equivalent JSON numbers do not match: %s and %s", group[0], input)
			}
		}
	}
	for _, pair := range [][2]string{
		{`{"n":9007199254740992}`, `{"n":9007199254740993}`},
		{`{"n":1e-400}`, `{"n":0}`},
		{`{"n":-1}`, `{"n":1}`},
		{`{"n":1}`, `{"n":"1"}`},
	} {
		if canonicalJSONDigest(pair[0]) == canonicalJSONDigest(pair[1]) {
			t.Errorf("distinct JSON values matched: %s and %s", pair[0], pair[1])
		}
	}
}

func TestCanonicalJSONDigestBindsInvalidAndOversizedInputExactly(t *testing.T) {
	for _, input := range []string{
		`{"n":1} trailing`,
		`{"n":1e1000001}`,
		`{"n":` + strings.Repeat("1", 4097) + `}`,
	} {
		if got, want := canonicalJSONDigest(input), digest([]byte("raw\x00"+input)); got != want {
			t.Errorf("unsafe or oversized input was normalized: %s", input[:min(len(input), 40)])
		}
	}
}

func TestCanonicalJSONDigestDuplicateKeysBindExactBytes(t *testing.T) {
	for _, duplicate := range []string{
		`{"a":1,"a":2}`,
		`{"outer":[{"a":1,"a":2}]}`,
		`{"a":1,"\u0061":2}`,
	} {
		if got, want := canonicalJSONDigest(duplicate), digest([]byte("raw\x00"+duplicate)); got != want {
			t.Errorf("duplicate keys were normalized: %s", duplicate)
		}
	}
	for _, pair := range [][2]string{
		{`{"a":1,"a":2}`, `{"a":2}`},
		{`{"a":1,"a":2}`, `{"a":1, "a":2}`},
		{`{"outer":[{"a":1,"a":2}]}`, `{"outer":[{"a":2}]}`},
		{`{"a":1,"\u0061":2}`, `{"a":2}`},
	} {
		if canonicalJSONDigest(pair[0]) == canonicalJSONDigest(pair[1]) {
			t.Errorf("ambiguous arguments matched: %s and %s", pair[0], pair[1])
		}
	}
}

func TestJSONFingerprintsRejectLossyUnicodeAndSeparateUserContent(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"s":"\ud800"}`, `{"s":"\ud801"}`},
		{string([]byte{'"', 0xff, '"'}), string([]byte{'"', 0xfe, '"'})},
		{`{"s":"\ud800"}`, `{"s":"�"}`},
	} {
		if canonicalJSONDigest(pair[0]) == canonicalJSONDigest(pair[1]) {
			t.Errorf("lossy strings matched: %q and %q", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]string{
		{`"\ud800"`, `"\ud801"`},
		{`[{"type":"image_url","image_url":{"url":"first","url":"second"}}]`, `[{"type":"image_url","image_url":{"url":"second"}}]`},
		{`[{"type":"image_url","image_url":{"url":"first","\u0075rl":"second"}}]`, `[{"type":"image_url","image_url":{"url":"second"}}]`},
		{`"[{\"type\":\"image_url\",\"image_url\":{\"url\":\"x\"}}]"`, `[{"type":"image_url","image_url":{"url":"x"}}]`},
	} {
		a, aok := chatUserContentFingerprint(gjson.Parse(pair[0]))
		b, bok := chatUserContentFingerprint(gjson.Parse(pair[1]))
		if !aok || !bok || a == b {
			t.Errorf("user values matched: %q and %q", pair[0], pair[1])
		}
	}
	if !jsonHasSafeStrings(`"\ud83d\ude00"`) || !jsonHasSafeStrings(`"\\ud800"`) {
		t.Fatal("valid Unicode or escaped backslash rejected")
	}
}

func TestTranslatedUserRejectsAmbiguousJSON(t *testing.T) {
	for _, test := range []struct{ source, input string }{
		{"openai-response", `{"input":[{"role":"user","content":"\ud800"}]}`},
		{"openai-response", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a","image_url":"https://example.test/b"}]}]}`},
		{"openai-response", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a","\u0069mage_url":"https://example.test/b"}]}]}`},
		{"openai-response", `{"input":[],"input":[{"role":"user","content":"hi"}]}`},
		{"openai-response", `{"input":[{"role":"assistant","role":"user","content":"hi"}]}`},
		{"claude", `{"messages":[{"role":"user","content":"\ud801"}]}`},
		{"claude", `{"messages":[{"role":"user","content":[{"type":"text","text":"a","text":"b"}]}]}`},
	} {
		translated, err := RequestForEndpointFrom(test.source, "gemini-3.8-flash", []byte(test.input), false, EndpointChatCompletions)
		if err == nil {
			t.Errorf("ambiguous source accepted: %s", translated)
		}
	}
}
