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
