package provider

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCapturedCopilotCatalogRegistersAssignedModels(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/current-copilot-catalog/response.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog modelListResponse
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Data) != 57 {
		t.Fatalf("captured catalogue rows = %d, want 57", len(catalog.Data))
	}
	service, _, storage := serviceWithCachedModels(t, catalog.Data)
	response, err := service.ModelsForAuth(context.Background(), "callback", pluginapi.AuthModelRequest{AuthID: "auth", StorageJSON: storage})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Models) != 18 {
		t.Fatalf("registered captured models = %d, want 18", len(response.Models))
	}
	registered := make(map[string]bool)
	for _, model := range response.Models {
		registered[model.ID] = true
	}
	for _, id := range []string{"gemini-3.8-flash", "gpt-6-luna", "claude-haiku-5.5"} {
		if !registered[id] {
			t.Errorf("assigned model %s was absent after catalogue registration", id)
		}
	}
	for _, model := range catalog.Data {
		if model.Policy != nil && model.Policy.State != "enabled" && registered[model.ID] {
			t.Errorf("non-enabled captured policy was bypassed for %s", model.ID)
		}
	}
}
