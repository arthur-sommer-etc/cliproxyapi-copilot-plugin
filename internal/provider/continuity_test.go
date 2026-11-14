package provider

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func continuityTestStorage(credential string) authStorage {
	root := bytes.Repeat([]byte{0x31}, 32)
	return authStorage{
		GitHubAccessToken: credential,
		GitHubUserID:      4242,
		ContinuityKeyring: &continuityKeyring{
			Version:               continuityKeyringVersion,
			KeyID:                 "fixed-continuity-key",
			AccountID:             4242,
			RootKey:               base64.RawURLEncoding.EncodeToString(root),
			CredentialFingerprint: tokenFingerprint(credential),
		},
	}
}

func TestContinuityKeyringRejectsCredentialAndAccountReplacement(t *testing.T) {
	storage := continuityTestStorage("credential-a")
	materials, err := continuityKeyMaterialsFor(storage, "auth-a", "model-a", "/responses", "https://api.example")
	if err != nil {
		t.Fatalf("valid keyring rejected: %v", err)
	}
	storage.GitHubAccessToken = "credential-b"
	if _, err := continuityKeyMaterialsFor(storage, "auth-a", "model-a", "/responses", "https://api.example"); err == nil {
		t.Fatal("keyring accepted an access-token replacement without OAuth refresh")
	}
	storage = continuityTestStorage("credential-a")
	storage.GitHubUserID++
	if _, err := continuityKeyMaterialsFor(storage, "auth-a", "model-a", "/responses", "https://api.example"); err == nil {
		t.Fatal("keyring accepted a changed GitHub account")
	}
	replacement := continuityTestStorage("credential-a")
	keyring, err := newContinuityKeyring(replacement.GitHubUserID, replacement.GitHubAccessToken)
	if err != nil {
		t.Fatalf("make replacement keyring: %v", err)
	}
	replacement.ContinuityKeyring = keyring
	replacementMaterials, err := continuityKeyMaterialsFor(replacement, "auth-a", "model-a", "/responses", "https://api.example")
	if err != nil {
		t.Fatalf("replacement keyring rejected: %v", err)
	}
	if materials.Active.Scope == replacementMaterials.Active.Scope || string(materials.Active.Secret) == string(replacementMaterials.Active.Secret) {
		t.Fatal("new login reused the prior account continuity root")
	}
}

func TestLegacyAuthUsesRefreshCompatibleArtifactKey(t *testing.T) {
	storage := authStorage{GitHubAccessToken: "legacy-credential", GitHubUserID: 4242}
	materials, err := continuityKeyMaterialsFor(storage, "auth-a", "model-a", "/responses", "https://api.example")
	if err != nil {
		t.Fatalf("legacy auth did not provide temporary artifact material: %v", err)
	}
	if !materials.activeIsLegacy || len(materials.Legacy) != 1 || materials.Active.Scope != materials.Legacy[0].Scope || string(materials.Active.Secret) != string(materials.Legacy[0].Secret) {
		t.Fatalf("legacy artifact material is not refresh-compatible: %#v", materials)
	}
	storage.ContinuityKeyring = &continuityKeyring{Version: continuityKeyringVersion}
	if _, err := continuityKeyMaterialsFor(storage, "auth-a", "model-a", "/responses", "https://api.example"); err == nil {
		t.Fatal("invalid persisted keyring used the temporary legacy key")
	}
}
