package provider

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/compact"
	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	translatorReasoningCarrierPrefix = "cpa-copilot-reasoning:v1:"
	reasoningCarrierNamespace        = "cpa-copilot-reasoning-auth:"
	sealedReasoningCarrierV1Prefix   = "cpa-copilot-reasoning-auth:v1:"
	sealedReasoningCarrierV2Prefix   = "cpa-copilot-reasoning-auth:v2:"
	reasoningCarrierMACDomainV1      = "cpa-copilot-bridge-reasoning-carrier-v1"
	reasoningCarrierMACDomainV2      = "cpa-copilot-bridge-reasoning-carrier-v2"
)

type reasoningCarrierScope struct {
	AuthID     string
	Model      string
	Endpoint   string
	APIBaseURL string
	Keys       artifactKeyMaterials
}

func reasoningCarrierScopeFor(authID, model, endpoint, apiBaseURL string, keys artifactKeyMaterials) reasoningCarrierScope {
	return reasoningCarrierScope{AuthID: authID, Model: model, Endpoint: endpoint, APIBaseURL: apiBaseURL, Keys: keys}
}

func unwrapRequestReasoningCarriers(sourceFormat string, payload []byte, scope reasoningCarrierScope) ([]byte, error) {
	return transformReasoningCarrierPaths(payload, requestReasoningCarrierPaths(sourceFormat, payload), func(value string) (string, bool, error) {
		if strings.HasPrefix(value, translatorReasoningCarrierPrefix) {
			return "", false, fmt.Errorf("unsealed Copilot reasoning carrier is not accepted")
		}
		if strings.HasPrefix(value, reasoningCarrierNamespace) {
			if !strings.HasPrefix(value, sealedReasoningCarrierV1Prefix) && !strings.HasPrefix(value, sealedReasoningCarrierV2Prefix) {
				return "", false, fmt.Errorf("unsupported Copilot reasoning carrier version")
			}
			inner, err := unsealReasoningCarrier(value, scope)
			return inner, err == nil, err
		}
		return value, false, nil
	})
}

func validateReasoningRequestForEndpoint(sourceFormat string, payload []byte, endpoint string) error {
	if endpoint != translate.EndpointChatCompletions {
		return nil
	}
	root := gjson.ParseBytes(payload)
	switch normalizeRequestFormat(sourceFormat) {
	case "claude":
		for _, message := range root.Get("messages").Array() {
			for _, block := range message.Get("content").Array() {
				switch block.Get("type").String() {
				case "thinking":
					signature := block.Get("signature")
					if signature.Type == gjson.String && signature.String() != "" && !strings.HasPrefix(signature.String(), translatorReasoningCarrierPrefix) {
						return fmt.Errorf("foreign Claude thinking signatures cannot be replayed to Copilot Chat")
					}
				case "redacted_thinking":
					data := block.Get("data")
					if data.Type == gjson.String && data.String() != "" {
						return fmt.Errorf("Claude redacted thinking cannot be replayed to Copilot Chat")
					}
				}
			}
		}
	case "openai-response":
		for _, item := range root.Get("input").Array() {
			if item.Get("type").String() != "reasoning" {
				continue
			}
			encryptedContent := item.Get("encrypted_content")
			if encryptedContent.Type == gjson.String && encryptedContent.String() != "" && !strings.HasPrefix(encryptedContent.String(), translatorReasoningCarrierPrefix) {
				return fmt.Errorf("foreign Responses reasoning cannot be replayed to Copilot Chat")
			}
		}
	}
	return nil
}

func sealResponseReasoningCarriers(sourceFormat string, payload []byte, scope reasoningCarrierScope) ([]byte, error) {
	return transformReasoningCarrierPaths(payload, responseReasoningCarrierPaths(sourceFormat, payload), func(value string) (string, bool, error) {
		if strings.HasPrefix(value, sealedReasoningCarrierV1Prefix) || strings.HasPrefix(value, sealedReasoningCarrierV2Prefix) {
			inner, err := unsealReasoningCarrier(value, scope)
			if err != nil {
				return "", false, err
			}
			sealed, err := sealReasoningCarrier(inner, scope)
			return sealed, err == nil, err
		}
		if strings.HasPrefix(value, translatorReasoningCarrierPrefix) {
			sealed, err := sealReasoningCarrier(value, scope)
			return sealed, err == nil, err
		}
		if strings.HasPrefix(value, reasoningCarrierNamespace) {
			return "", false, fmt.Errorf("unsupported Copilot reasoning carrier version")
		}
		return value, false, nil
	})
}

func sealReasoningCarrier(inner string, scope reasoningCarrierScope) (string, error) {
	if !strings.HasPrefix(inner, translatorReasoningCarrierPrefix) {
		return "", fmt.Errorf("invalid Copilot reasoning carrier")
	}
	secret, scopeID, err := reasoningCarrierKeyMaterial(scope.Keys.Active)
	if err != nil {
		return "", err
	}
	mac := reasoningCarrierMAC(secret, scopeID, []byte(inner), reasoningCarrierMACDomainV2)
	return sealedReasoningCarrierV2Prefix + base64.RawURLEncoding.EncodeToString([]byte(inner)) + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

func unsealReasoningCarrier(sealed string, scope reasoningCarrierScope) (string, error) {
	var prefix string
	var keys []compact.KeyMaterial
	var domain string
	switch {
	case strings.HasPrefix(sealed, sealedReasoningCarrierV2Prefix):
		prefix = sealedReasoningCarrierV2Prefix
		keys = []compact.KeyMaterial{scope.Keys.Active}
		domain = reasoningCarrierMACDomainV2
	case strings.HasPrefix(sealed, sealedReasoningCarrierV1Prefix):
		prefix = sealedReasoningCarrierV1Prefix
		keys = scope.Keys.Legacy
		domain = reasoningCarrierMACDomainV1
	default:
		return "", fmt.Errorf("invalid Copilot reasoning carrier")
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("Copilot reasoning carrier scope is unavailable")
	}
	encoded := strings.TrimPrefix(sealed, prefix)
	payloadPart, macPart, ok := strings.Cut(encoded, ".")
	if !ok || payloadPart == "" || macPart == "" || strings.Contains(macPart, ".") {
		return "", fmt.Errorf("invalid Copilot reasoning carrier")
	}
	innerBytes, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil || !strings.HasPrefix(string(innerBytes), translatorReasoningCarrierPrefix) {
		return "", fmt.Errorf("invalid Copilot reasoning carrier")
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(macPart)
	if err != nil || len(providedMAC) != sha256.Size {
		return "", fmt.Errorf("invalid Copilot reasoning carrier")
	}
	for _, key := range keys {
		secret, scopeID, err := reasoningCarrierKeyMaterial(key)
		if err != nil {
			continue
		}
		if hmac.Equal(providedMAC, reasoningCarrierMAC(secret, scopeID, innerBytes, domain)) {
			return string(innerBytes), nil
		}
	}
	return "", fmt.Errorf("Copilot reasoning carrier scope or authentication is invalid")
}

func reasoningCarrierKeyMaterial(key compact.KeyMaterial) ([]byte, string, error) {
	if strings.TrimSpace(key.Scope) == "" || len(key.Secret) != sha256.Size {
		return nil, "", fmt.Errorf("Copilot reasoning carrier scope is unavailable")
	}
	return key.Secret, key.Scope, nil
}

func reasoningCarrierMAC(secret []byte, scopeID string, inner []byte, domain string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(scopeID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(inner)
	return mac.Sum(nil)
}

func requestReasoningCarrierPaths(sourceFormat string, payload []byte) []string {
	root := gjson.ParseBytes(payload)
	paths := make([]string, 0)
	switch normalizeRequestFormat(sourceFormat) {
	case "claude":
		for messageIndex, message := range root.Get("messages").Array() {
			for blockIndex, block := range message.Get("content").Array() {
				if block.Get("type").String() == "thinking" && block.Get("signature").Type == gjson.String {
					paths = append(paths, fmt.Sprintf("messages.%d.content.%d.signature", messageIndex, blockIndex))
				}
			}
		}
	case "openai-response":
		for index, item := range root.Get("input").Array() {
			if item.Get("type").String() == "reasoning" && item.Get("encrypted_content").Type == gjson.String {
				paths = append(paths, fmt.Sprintf("input.%d.encrypted_content", index))
			}
		}
	}
	return paths
}

func responseReasoningCarrierPaths(sourceFormat string, payload []byte) []string {
	root := gjson.ParseBytes(payload)
	paths := make([]string, 0)
	switch normalizeRequestFormat(sourceFormat) {
	case "claude":
		appendClaudeThinkingSignaturePaths(&paths, root.Get("content"), "content")
	case "openai-response":
		appendResponsesEncryptedContentPaths(&paths, root.Get("output"), "output")
	}
	return paths
}

func streamReasoningCarrierPaths(destination string, payload []byte) []string {
	root := gjson.ParseBytes(payload)
	paths := make([]string, 0)
	switch normalizeRequestFormat(destination) {
	case "claude":
		if root.Get("content_block.type").String() == "thinking" {
			paths = appendStringPath(&paths, root.Get("content_block.signature"), "content_block.signature")
		}
		if root.Get("delta.type").String() == "signature_delta" {
			paths = appendStringPath(&paths, root.Get("delta.signature"), "delta.signature")
		}
		appendClaudeThinkingSignaturePaths(&paths, root.Get("content"), "content")
	case "openai-response":
		if root.Get("item.type").String() == "reasoning" {
			paths = appendStringPath(&paths, root.Get("item.encrypted_content"), "item.encrypted_content")
		}
		appendResponsesEncryptedContentPaths(&paths, root.Get("output"), "output")
		appendResponsesEncryptedContentPaths(&paths, root.Get("response.output"), "response.output")
	}
	return paths
}

func appendClaudeThinkingSignaturePaths(paths *[]string, blocks gjson.Result, base string) {
	for index, block := range blocks.Array() {
		if block.Get("type").String() == "thinking" && block.Get("signature").Type == gjson.String {
			*paths = append(*paths, fmt.Sprintf("%s.%d.signature", base, index))
		}
	}
}

func appendResponsesEncryptedContentPaths(paths *[]string, items gjson.Result, base string) {
	for index, item := range items.Array() {
		if item.Get("type").String() == "reasoning" && item.Get("encrypted_content").Type == gjson.String {
			*paths = append(*paths, fmt.Sprintf("%s.%d.encrypted_content", base, index))
		}
	}
}

func appendStringPath(paths *[]string, value gjson.Result, path string) []string {
	if value.Type == gjson.String {
		*paths = append(*paths, path)
	}
	return *paths
}

func transformReasoningCarrierPaths(payload []byte, paths []string, transform func(string) (string, bool, error)) ([]byte, error) {
	if len(paths) == 0 {
		return payload, nil
	}
	updated := payload
	for _, path := range paths {
		value := gjson.GetBytes(payload, path)
		if value.Type != gjson.String {
			continue
		}
		transformed, changed, err := transform(value.String())
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		var errSet error
		updated, errSet = sjson.SetBytes(updated, path, transformed)
		if errSet != nil {
			return nil, fmt.Errorf("update Copilot reasoning carrier: %w", errSet)
		}
	}
	return updated, nil
}

func sealReasoningCarrierSSEFrame(destination string, frame []byte, scope reasoningCarrierScope) ([]byte, error) {
	var output bytes.Buffer
	changed := false
	for offset := 0; offset < len(frame); {
		lineEnd := bytes.IndexByte(frame[offset:], '\n')
		lineLength := len(frame) - offset
		if lineEnd >= 0 {
			lineLength = lineEnd + 1
		}
		line := frame[offset : offset+lineLength]
		contentEnd := len(line)
		if contentEnd > 0 && line[contentEnd-1] == '\n' {
			contentEnd--
		}
		if contentEnd > 0 && line[contentEnd-1] == '\r' {
			contentEnd--
		}
		content := line[:contentEnd]
		if bytes.HasPrefix(content, []byte("data:")) {
			dataStart := len("data:")
			if dataStart < len(content) && content[dataStart] == ' ' {
				dataStart++
			}
			payload := content[dataStart:]
			if gjson.ValidBytes(payload) {
				sealed, err := sealStreamReasoningCarriers(destination, payload, scope)
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(sealed, payload) {
					output.Write(line[:dataStart])
					output.Write(sealed)
					output.Write(line[contentEnd:])
					changed = true
					offset += lineLength
					continue
				}
			}
		}
		output.Write(line)
		offset += lineLength
	}
	if !changed {
		return frame, nil
	}
	return output.Bytes(), nil
}

func sealStreamReasoningCarriers(destination string, payload []byte, scope reasoningCarrierScope) ([]byte, error) {
	return transformReasoningCarrierPaths(payload, streamReasoningCarrierPaths(destination, payload), func(value string) (string, bool, error) {
		if strings.HasPrefix(value, sealedReasoningCarrierV1Prefix) || strings.HasPrefix(value, sealedReasoningCarrierV2Prefix) {
			inner, err := unsealReasoningCarrier(value, scope)
			if err != nil {
				return "", false, err
			}
			sealed, err := sealReasoningCarrier(inner, scope)
			return sealed, err == nil, err
		}
		if strings.HasPrefix(value, translatorReasoningCarrierPrefix) {
			sealed, err := sealReasoningCarrier(value, scope)
			return sealed, err == nil, err
		}
		if strings.HasPrefix(value, reasoningCarrierNamespace) {
			return "", false, fmt.Errorf("unsupported Copilot reasoning carrier version")
		}
		return value, false, nil
	})
}
