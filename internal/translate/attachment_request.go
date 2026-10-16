package translate

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type requestAttachment struct {
	part     map[string]any
	kind     string
	data     string
	filename string
	detail   string
}

// UnsupportedImageDetailError identifies image detail that cannot be preserved
// by a destination endpoint.
type UnsupportedImageDetailError struct {
	Endpoint string
	Detail   string
}

func (e *UnsupportedImageDetailError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("image detail %q cannot be represented by endpoint %s", e.Detail, e.Endpoint)
}

// ValidateRequestAttachmentsForEndpoint checks whether attachment semantics can
// be preserved when translating a request to the selected Copilot endpoint.
func ValidateRequestAttachmentsForEndpoint(source, endpoint string, body []byte) error {
	from := sdktranslator.FromString(source)
	if from == "" {
		return fmt.Errorf("unsupported request source format %q", source)
	}
	to, err := endpointFormat(endpoint)
	if err != nil {
		return err
	}
	if from == to {
		return nil
	}
	return validateRequestAttachments(body, from, to)
}

func requestAttachments(root map[string]any, format sdktranslator.Format) ([]requestAttachment, error) {
	var attachments []requestAttachment
	var visit func(any) error
	visit = func(value any) error {
		for _, rawPart := range arrayValue(value) {
			part := objectValue(rawPart)
			attachment := requestAttachment{part: part}
			switch stringValue(part["type"]) {
			case "tool_result":
				if format == sdktranslator.FormatClaude {
					if err := visit(part["content"]); err != nil {
						return err
					}
				}
			case "image_url":
				if format == sdktranslator.FormatOpenAI {
					image := objectValue(part["image_url"])
					attachment.kind, attachment.data, attachment.detail = "image", rawStringValue(image["url"]), rawStringValue(image["detail"])
				}
			case "file":
				if format == sdktranslator.FormatOpenAI {
					file := objectValue(part["file"])
					attachment.kind, attachment.data, attachment.filename = "file", rawStringValue(file["file_data"]), rawStringValue(file["filename"])
				}
			case "input_image":
				if format == sdktranslator.FormatOpenAIResponse {
					attachment.kind, attachment.data, attachment.detail = "image", firstNonEmptyString(rawStringValue(part["image_url"]), rawStringValue(part["url"])), rawStringValue(part["detail"])
				}
			case "input_file":
				if format == sdktranslator.FormatOpenAIResponse {
					attachment.kind, attachment.data, attachment.filename = "file", firstNonEmptyString(rawStringValue(part["file_data"]), rawStringValue(part["file_url"])), rawStringValue(part["filename"])
				}
			case "image", "document":
				if format == sdktranslator.FormatClaude {
					source := objectValue(part["source"])
					attachment.kind = "image"
					if stringValue(part["type"]) == "document" {
						attachment.kind, attachment.filename = "file", rawStringValue(part["title"])
					}
					switch stringValue(source["type"]) {
					case "base64":
						attachment.data = "data:" + firstNonEmptyString(rawStringValue(source["media_type"]), "application/octet-stream") + ";base64," + rawStringValue(source["data"])
					case "url":
						attachment.data = rawStringValue(source["url"])
					case "text":
						attachment.data = rawStringValue(source["data"])
					case "file":
						return fmt.Errorf("uploaded attachment references cannot be translated across endpoints")
					}
				}
			}
			if attachment.kind != "" {
				attachments = append(attachments, attachment)
			}
		}
		return nil
	}
	if format == sdktranslator.FormatOpenAIResponse {
		for _, rawItem := range arrayValue(root["input"]) {
			item := objectValue(rawItem)
			if err := visit(item["content"]); err != nil {
				return nil, err
			}
			if typ := stringValue(item["type"]); typ == "function_call_output" || typ == "custom_tool_call_output" {
				if err := visit(item["output"]); err != nil {
					return nil, err
				}
			}
		}
	} else {
		for _, rawMessage := range arrayValue(root["messages"]) {
			message := objectValue(rawMessage)
			if format == sdktranslator.FormatClaude && stringValue(message["role"]) == "system" {
				continue
			}
			if err := visit(message["content"]); err != nil {
				return nil, err
			}
		}
	}
	return attachments, nil
}

func validateRequestAttachments(body []byte, from, to sdktranslator.Format) error {
	root, err := decodeObject(body)
	if err != nil {
		return err
	}
	attachments, err := requestAttachments(root, from)
	if err != nil {
		return err
	}
	for _, attachment := range attachments {
		if attachment.kind == "image" {
			fields := attachment.part
			if from == sdktranslator.FormatOpenAI {
				fields = objectValue(fields["image_url"])
				if err := validateAttachmentFields(fields, "url", "detail"); err != nil {
					return err
				}
			} else if from == sdktranslator.FormatOpenAIResponse {
				if err := validateAttachmentFields(fields, "type", "image_url", "url", "detail", "cache_control"); err != nil {
					return err
				}
			}
			if value, exists := fields["detail"]; exists && value != nil {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("image detail must be a string")
				}
			}
			switch attachment.detail {
			case "", "auto", "low", "high":
			case "original":
				if from == sdktranslator.FormatOpenAI {
					return &UnsupportedImageDetailError{Endpoint: endpointForFormat(to), Detail: attachment.detail}
				}
				if to == sdktranslator.FormatOpenAI {
					return &UnsupportedImageDetailError{Endpoint: EndpointChatCompletions, Detail: attachment.detail}
				}
			default:
				return fmt.Errorf("image detail cannot be represented by the destination endpoint")
			}
			if attachment.data == "" {
				return fmt.Errorf("image attachment requires a URL or inline data")
			}
			if to == sdktranslator.FormatClaude && attachment.detail != "" && attachment.detail != "auto" {
				return &UnsupportedImageDetailError{Endpoint: EndpointMessages, Detail: attachment.detail}
			}
			if strings.HasPrefix(attachment.data, "data:") {
				if _, err := inlineAttachmentData(attachment.data, "image/"); err != nil {
					return err
				}
			}
			continue
		}
		fields := attachment.part
		allowed := []string{"type", "filename", "file_data", "cache_control"}
		switch from {
		case sdktranslator.FormatOpenAI:
			if err := validateAttachmentFields(fields, "type", "file", "cache_control"); err != nil {
				return err
			}
			fields = objectValue(fields["file"])
			allowed = []string{"filename", "file_data"}
		case sdktranslator.FormatClaude:
			allowed = []string{"type", "source", "title", "cache_control"}
			source := objectValue(fields["source"])
			if err := validateAttachmentFields(source, "type", "media_type", "data", "url"); err != nil {
				return err
			}
			if stringValue(source["type"]) == "url" && to == sdktranslator.FormatOpenAIResponse {
				if attachment.data == "" {
					return fmt.Errorf("document URL is empty")
				}
			} else if stringValue(source["type"]) != "base64" {
				return fmt.Errorf("document source cannot be represented by the destination endpoint")
			}
		}
		if err := validateAttachmentFields(fields, allowed...); err != nil {
			return err
		}
		for _, name := range []string{"filename", "title"} {
			if value, exists := fields[name]; exists && value != nil {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("attachment filename must be a string")
				}
			}
		}
		if from == sdktranslator.FormatClaude && stringValue(objectValue(attachment.part["source"])["type"]) == "url" && to == sdktranslator.FormatOpenAIResponse {
			continue
		}
		if _, err := inlineAttachmentData(attachment.data, "application/pdf"); err != nil {
			return err
		}
	}
	return nil
}

func validateAttachmentFields(part map[string]any, allowed ...string) error {
	for key, value := range part {
		known := false
		for _, name := range allowed {
			known = known || key == name
		}
		if !known && hasMeaningfulValue(value) {
			return fmt.Errorf("attachment field %q cannot be represented by the destination endpoint", key)
		}
	}
	return nil
}

func inlineAttachmentData(data, mediaType string) ([]byte, error) {
	parts := strings.SplitN(strings.TrimPrefix(data, "data:"), ";base64,", 2)
	if !strings.HasPrefix(data, "data:") || len(parts) != 2 || parts[1] == "" {
		return nil, fmt.Errorf("attachment requires inline base64 data")
	}
	if mediaType == "image/" && !strings.HasPrefix(parts[0], mediaType) || mediaType != "image/" && parts[0] != mediaType {
		return nil, fmt.Errorf("attachment media type cannot be represented by the destination endpoint")
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("attachment contains invalid base64 data")
	}
	return decoded, nil
}

func restoreRequestAttachmentFields(original, converted []byte, from, to sdktranslator.Format) ([]byte, error) {
	source, err := decodeObject(original)
	if err != nil {
		return nil, err
	}
	attachments, err := requestAttachments(source, from)
	if err != nil || len(attachments) == 0 {
		return converted, err
	}
	root, err := decodeObject(converted)
	if err != nil {
		return nil, err
	}
	translated, err := requestAttachments(root, to)
	if err != nil {
		return nil, err
	}
	if len(attachments) != len(translated) {
		return nil, fmt.Errorf("request attachment count changed during translation")
	}
	for index, attachment := range attachments {
		output := translated[index]
		if attachment.kind != output.kind || attachment.data != output.data {
			return nil, fmt.Errorf("request attachment content changed during translation")
		}
		if attachment.kind == "file" && attachment.filename != "" {
			switch to {
			case sdktranslator.FormatOpenAI:
				objectValue(output.part["file"])["filename"] = attachment.filename
			case sdktranslator.FormatOpenAIResponse:
				output.part["filename"] = attachment.filename
			case sdktranslator.FormatClaude:
				output.part["title"] = attachment.filename
			}
		}
		if attachment.kind == "image" && attachment.detail != "" {
			switch to {
			case sdktranslator.FormatOpenAI:
				if attachment.detail == "original" {
					return nil, fmt.Errorf("original image detail cannot be represented by Chat Completions")
				}
				objectValue(output.part["image_url"])["detail"] = attachment.detail
			case sdktranslator.FormatOpenAIResponse:
				output.part["detail"] = attachment.detail
			}
		}
	}
	return json.Marshal(root)
}

func prepareResponsesChatAttachments(body []byte) ([]byte, map[string]map[string]any, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, nil, err
	}
	files := make(map[string]map[string]any)
	changed := false
	digest := sha256.Sum256(body)
	for _, rawItem := range arrayValue(root["input"]) {
		item := objectValue(rawItem)
		if typ := stringValue(item["type"]); typ == "function_call_output" || typ == "custom_tool_call_output" {
			for _, rawPart := range arrayValue(item["output"]) {
				if stringValue(objectValue(rawPart)["type"]) == "input_file" {
					return nil, nil, fmt.Errorf("tool output files cannot be represented by Chat Completions")
				}
			}
		}
		for index, rawPart := range arrayValue(item["content"]) {
			part := objectValue(rawPart)
			switch stringValue(part["type"]) {
			case "input_image":
				if rawStringValue(part["image_url"]) == "" && rawStringValue(part["url"]) != "" {
					part["image_url"] = part["url"]
					delete(part, "url")
					changed = true
				}
			case "input_file":
				if _, err := inlineAttachmentData(rawStringValue(part["file_data"]), "application/pdf"); err != nil {
					return nil, nil, err
				}
				marker := fmt.Sprintf("cpa-inline-pdf:%x:%d", digest, len(files))
				if bytes.Contains(body, []byte(marker)) {
					return nil, nil, fmt.Errorf("attachment marker collides with request content")
				}
				file := map[string]any{"file_data": part["file_data"]}
				if filename, exists := part["filename"]; exists {
					file["filename"] = filename
				}
				files[marker] = map[string]any{"type": "file", "file": file}
				arrayValue(item["content"])[index] = map[string]any{"type": "input_text", "text": marker}
				changed = true
			}
		}
	}
	if !changed {
		return body, files, nil
	}
	prepared, err := json.Marshal(root)
	return prepared, files, err
}

func restoreChatAttachmentFiles(body []byte, files map[string]map[string]any) ([]byte, error) {
	if len(files) == 0 {
		return body, nil
	}
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	restored := make(map[string]bool, len(files))
	for _, rawMessage := range arrayValue(root["messages"]) {
		message := objectValue(rawMessage)
		for index, rawPart := range arrayValue(message["content"]) {
			part := objectValue(rawPart)
			marker := rawStringValue(part["text"])
			if file, exists := files[marker]; exists && stringValue(part["type"]) == "text" {
				if restored[marker] {
					return nil, fmt.Errorf("inline PDF attachment was duplicated during Chat translation")
				}
				arrayValue(message["content"])[index] = file
				restored[marker] = true
			}
		}
	}
	if len(restored) != len(files) {
		return nil, fmt.Errorf("inline PDF attachment was lost during Chat translation")
	}
	return json.Marshal(root)
}
