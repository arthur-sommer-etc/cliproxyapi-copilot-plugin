package main

import "testing"

func TestPluginRegistrationDeclaresQuotaProvider(t *testing.T) {
	t.Parallel()

	if !pluginRegistration().Capabilities.QuotaProvider {
		t.Fatal("quota_provider is not declared")
	}
}
