package integration

import (
	"slices"
	"strings"
	"testing"
)

func filteredChildEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "PATH", "HOME", "SYSTEMROOT", "WINDIR", "TMP", "TEMP", "TMPDIR", "LANG", "LC_ALL", "TZ":
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func TestFilteredChildEnvironment(t *testing.T) {
	environment := []string{
		"PATH=/fixture/bin",
		"HOME=/fixture/home",
		"SYSTEMROOT=/fixture/windows",
		"WINDIR=/fixture/windows",
		"TMP=/fixture/tmp",
		"TEMP=/fixture/tmp",
		"TMPDIR=/fixture/tmp",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TZ=UTC",
		"PGSTORE_URL=fixture-value",
		"pgstore_password=fixture-value",
		"GITSTORE_TOKEN=fixture-value",
		"OBJECTSTORE_SECRET=fixture-value",
		"GH_TOKEN=fixture-value",
		"GITHUB_TOKEN=fixture-value",
		"OPENAI_API_KEY=fixture-value",
		"ANTHROPIC_API_KEY=fixture-value",
		"AWS_ACCESS_KEY_ID=fixture-value",
		"AWS_SECRET_ACCESS_KEY=fixture-value",
		"HOME_JWT=fixture-value",
		"home_jwt=fixture-value",
		"DEPLOY=fixture-value",
		"deploy=fixture-value",
		"MANAGEMENT_PASSWORD=fixture-value",
		"management_password=fixture-value",
		"DEPLOYMENT=preserved-value",
		"APP_MODE=test",
		"gItHuB_tOkEn=fixture-value",
	}
	want := []string{
		"PATH=/fixture/bin",
		"HOME=/fixture/home",
		"SYSTEMROOT=/fixture/windows",
		"WINDIR=/fixture/windows",
		"TMP=/fixture/tmp",
		"TEMP=/fixture/tmp",
		"TMPDIR=/fixture/tmp",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TZ=UTC",
	}
	if got := filteredChildEnvironment(environment); !slices.Equal(got, want) {
		t.Fatalf("filtered child environment = %q, want %q", got, want)
	}
}
