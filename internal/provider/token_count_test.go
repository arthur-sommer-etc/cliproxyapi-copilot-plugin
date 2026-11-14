package provider

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestCountTokensIncludesFlatToolSchemas(t *testing.T) {
	t.Parallel()
	schema := `{"type":"object","properties":{"city":{"type":"string","description":"The city whose current weather should be retrieved."}},"required":["city"]}`
	request := func(fields string) []byte {
		return []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"Check the weather."}],"tools":[{"name":"weather","description":"Look up the weather for a city.",%s}]}`, fields))
	}
	count := func(original, payload []byte) int64 {
		t.Helper()
		response, err := (&Service{}).CountTokens(ExecuteRequest{ExecutorRequest: pluginapi.ExecutorRequest{
			SourceFormat: "claude", OriginalRequest: original, Payload: payload,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return gjson.GetBytes(response.Payload, "input_tokens").Int()
	}
	native := request(`"input_schema":` + schema)
	want := count(native, nil)
	if withoutSchema := count(request(`"input_schema":{}`), nil); want <= withoutSchema {
		t.Fatalf("schema did not contribute to the estimate: schema=%d empty=%d", want, withoutSchema)
	}
	for _, test := range []struct {
		name     string
		original []byte
		payload  []byte
	}{
		{"flat parameters", request(`"parameters":` + schema), nil},
		{"matching aliases", request(`"input_schema":` + schema + `,"parameters":` + schema), nil},
		{"payload fallback", nil, request(`"parameters":` + schema)},
		{"original precedence", native, request(`"parameters":{}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := count(test.original, test.payload); got != want {
				t.Fatalf("input_tokens=%d, want equivalent-schema estimate %d", got, want)
			}
		})
	}
}
