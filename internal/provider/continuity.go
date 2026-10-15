package provider

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/compact"
)

const continuityKeyringVersion = 1

var errContinuityKeyringUnavailable = errors.New("Copilot continuity keyring is missing or invalid")

type continuityKeyring struct {
	Version               int                  `json:"version"`
	KeyID                 string               `json:"key_id"`
	AccountID             int64                `json:"account_id"`
	RootKey               string               `json:"root_key"`
	CredentialFingerprint string               `json:"credential_fingerprint"`
	LegacyV1              *legacyContinuityKey `json:"legacy_v1,omitempty"`
}

type legacyContinuityKey struct {
	AccountID             int64  `json:"account_id"`
	AuthID                string `json:"auth_id"`
	CredentialFingerprint string `json:"credential_fingerprint"`
	APIBaseURL            string `json:"api_base_url"`
	EndpointPending       bool   `json:"endpoint_pending,omitempty"`
}

type artifactKeyMaterials struct {
	Active compact.KeyMaterial
	Legacy []compact.KeyMaterial
}

func newContinuityKeyring(accountID int64, credential string) (*continuityKeyring, error) {
	if accountID <= 0 || strings.TrimSpace(credential) == "" {
		return nil, errContinuityKeyringUnavailable
	}
	root := make([]byte, sha256.Size)
	if _, err := rand.Read(root); err != nil {
		return nil, fmt.Errorf("generate Copilot continuity keyring: %w", err)
	}
	keyID := make([]byte, 16)
	if _, err := rand.Read(keyID); err != nil {
		return nil, fmt.Errorf("generate Copilot continuity key ID: %w", err)
	}
	return &continuityKeyring{
		Version:               continuityKeyringVersion,
		KeyID:                 hex.EncodeToString(keyID),
		AccountID:             accountID,
		RootKey:               base64.RawURLEncoding.EncodeToString(root),
		CredentialFingerprint: tokenFingerprint(credential),
	}, nil
}

func validateContinuityKeyring(storage authStorage, authID string) ([]byte, error) {
	keyring := storage.ContinuityKeyring
	if keyring == nil || keyring.Version != continuityKeyringVersion || strings.TrimSpace(keyring.KeyID) == "" ||
		keyring.AccountID <= 0 || storage.GitHubUserID != keyring.AccountID ||
		keyring.CredentialFingerprint != tokenFingerprint(storage.GitHubAccessToken) {
		return nil, errContinuityKeyringUnavailable
	}
	root, errDecode := base64.RawURLEncoding.DecodeString(keyring.RootKey)
	if errDecode != nil || len(root) != sha256.Size {
		return nil, errContinuityKeyringUnavailable
	}
	if legacy := keyring.LegacyV1; legacy != nil {
		credentialKey, errCredential := hex.DecodeString(legacy.CredentialFingerprint)
		if legacy.AccountID != keyring.AccountID || strings.TrimSpace(legacy.AuthID) == "" || strings.TrimSpace(authID) != legacy.AuthID ||
			errCredential != nil || len(credentialKey) != sha256.Size ||
			(legacy.EndpointPending != (strings.TrimSpace(legacy.APIBaseURL) == "")) {
			return nil, errContinuityKeyringUnavailable
		}
	}
	return root, nil
}

func addLegacyContinuityKey(keyring *continuityKeyring, authID, credential, apiBaseURL string) {
	if keyring == nil || keyring.AccountID <= 0 || strings.TrimSpace(authID) == "" || strings.TrimSpace(credential) == "" || strings.TrimSpace(apiBaseURL) == "" {
		return
	}
	keyring.LegacyV1 = &legacyContinuityKey{
		AccountID:             keyring.AccountID,
		AuthID:                strings.TrimSpace(authID),
		CredentialFingerprint: tokenFingerprint(credential),
		APIBaseURL:            normalizeContinuityAPIBase(apiBaseURL),
	}
}

func continuityKeyMaterialsFor(storage authStorage, authID, model, endpoint, apiBaseURL string) (artifactKeyMaterials, error) {
	root, errKeyring := validateContinuityKeyring(storage, authID)
	if errKeyring != nil {
		return artifactKeyMaterials{}, errKeyring
	}
	parts := []string{
		"cpa-copilot-bridge/continuity/scope/v1",
		storage.ContinuityKeyring.KeyID,
		fmt.Sprint(storage.GitHubUserID),
		strings.TrimSpace(authID),
		strings.ToLower(strings.TrimSpace(model)),
		strings.TrimSpace(endpoint),
		normalizeContinuityAPIBase(apiBaseURL),
	}
	scopeHash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	materials := artifactKeyMaterials{Active: compact.KeyMaterial{Scope: hex.EncodeToString(scopeHash[:]), Secret: root}}
	legacy := storage.ContinuityKeyring.LegacyV1
	if legacy == nil || legacy.AccountID != storage.GitHubUserID || legacy.AuthID != strings.TrimSpace(authID) ||
		(!legacy.EndpointPending && !sameCopilotAPIBaseURL(legacy.APIBaseURL, apiBaseURL)) {
		return materials, nil
	}
	credentialKey, errCredential := hex.DecodeString(legacy.CredentialFingerprint)
	if errCredential != nil || len(credentialKey) != sha256.Size {
		return artifactKeyMaterials{}, errContinuityKeyringUnavailable
	}
	legacyAPIBase := legacy.APIBaseURL
	if legacy.EndpointPending {
		legacyAPIBase = apiBaseURL
	}
	scope, secret := compactionKeyMaterialFromFingerprint(authID, legacy.CredentialFingerprint, model, endpoint, legacyAPIBase)
	materials.Legacy = []compact.KeyMaterial{{Scope: scope, Secret: secret}}
	return materials, nil
}

func normalizeContinuityAPIBase(apiBaseURL string) string {
	return strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
}
