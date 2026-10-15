package translate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesChatCitation struct {
	StartIndex int64  `json:"start_index"`
	EndIndex   int64  `json:"end_index"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

func responsesURLCitation(value any) (responsesChatCitation, error) {
	annotation, ok := value.(map[string]any)
	if !ok {
		return responsesChatCitation{}, fmt.Errorf("Responses URL citation has an unsupported shape")
	}
	if err := rejectFields(annotation, "type", "start_index", "end_index", "title", "url"); err != nil {
		return responsesChatCitation{}, fmt.Errorf("Responses annotations have an unsupported field")
	}
	if stringValue(annotation["type"]) != "url_citation" {
		return responsesChatCitation{}, fmt.Errorf("unsupported Responses annotations type %q", stringValue(annotation["type"]))
	}
	start, startOK := annotation["start_index"].(json.Number)
	end, endOK := annotation["end_index"].(json.Number)
	startIndex, startErr := strconv.ParseInt(string(start), 10, 64)
	endIndex, endErr := strconv.ParseInt(string(end), 10, 64)
	title, titleOK := annotation["title"].(string)
	url, urlOK := annotation["url"].(string)
	if !startOK || !endOK || startErr != nil || endErr != nil || !titleOK || !urlOK || url == "" {
		return responsesChatCitation{}, fmt.Errorf("Responses URL citation has incomplete or invalid fields")
	}
	if startIndex < 0 || endIndex < startIndex {
		return responsesChatCitation{}, fmt.Errorf("Responses URL citation has incoherent character indices")
	}
	return responsesChatCitation{StartIndex: startIndex, EndIndex: endIndex, Title: title, URL: url}, nil
}

// prepareResponsesChatCitations removes standard annotations only for the SDK
// conversion hop and records their offsets against the concatenated Chat text.
func prepareResponsesChatCitations(body []byte) ([]byte, string, []responsesChatCitation, error) {
	out := body
	var expected strings.Builder
	var citations []responsesChatCitation
	var base int64
	for itemIndex, item := range gjson.GetBytes(body, "output").Array() {
		if item.Get("type").String() != "message" {
			continue
		}
		for partIndex, part := range item.Get("content").Array() {
			typ := part.Get("type").String()
			text := ""
			switch typ {
			case "output_text", "text":
				text = part.Get("text").String()
			case "refusal":
				text = part.Get("refusal").String()
			default:
				continue
			}
			partRunes := int64(utf8.RuneCountInString(text))
			annotations := part.Get("annotations")
			var annotationItems []gjson.Result
			if annotations.IsArray() {
				annotationItems = annotations.Array()
			}
			for _, annotation := range annotationItems {
				object, err := decodeObject([]byte(annotation.Raw))
				if err != nil {
					return nil, "", nil, fmt.Errorf("decode Responses URL citation")
				}
				citation, err := responsesURLCitation(object)
				if err != nil {
					return nil, "", nil, err
				}
				if typ != "output_text" && typ != "text" || citation.StartIndex >= partRunes || citation.EndIndex > partRunes {
					return nil, "", nil, fmt.Errorf("Responses URL citation is outside representable text")
				}
				citation.StartIndex += base
				citation.EndIndex += base
				citations = append(citations, citation)
			}
			if annotations.IsArray() && len(annotations.Array()) > 0 {
				var err error
				out, err = sjson.SetBytes(out, fmt.Sprintf("output.%d.content.%d.annotations", itemIndex, partIndex), []any{})
				if err != nil {
					return nil, "", nil, fmt.Errorf("prepare Responses URL citations for Chat conversion")
				}
			}
			expected.WriteString(text)
			base += partRunes
		}
	}
	if len(citations) == 0 {
		return body, "", nil, nil
	}
	return out, expected.String(), citations, nil
}

func restoreResponsesChatCitations(chatResponse []byte, expectedText string, citations []responsesChatCitation) ([]byte, error) {
	if len(citations) == 0 {
		return chatResponse, nil
	}
	choices := gjson.GetBytes(chatResponse, "choices")
	if !choices.IsArray() || len(choices.Array()) != 1 {
		return nil, fmt.Errorf("Responses URL citations cannot be anchored to one Chat choice")
	}
	message := choices.Array()[0].Get("message")
	if message.Get("content").Type != gjson.String || message.Get("content").String() != expectedText {
		return nil, fmt.Errorf("Responses URL citations cannot be anchored to the converted Chat text")
	}
	if existing := message.Get("annotations"); existing.Exists() && (!existing.IsArray() || len(existing.Array()) != 0) {
		return nil, fmt.Errorf("Chat conversion produced conflicting message annotations")
	}
	annotations := make([]map[string]any, 0, len(citations))
	for _, citation := range citations {
		annotations = append(annotations, map[string]any{
			"type": "url_citation",
			"url_citation": map[string]any{
				"start_index": citation.StartIndex,
				"end_index":   citation.EndIndex,
				"title":       citation.Title,
				"url":         citation.URL,
			},
		})
	}
	raw, err := json.Marshal(annotations)
	if err != nil {
		return nil, fmt.Errorf("encode Chat URL citations")
	}
	out, err := sjson.SetRawBytes(chatResponse, "choices.0.message.annotations", raw)
	if err != nil {
		return nil, fmt.Errorf("restore Responses URL citations in Chat response")
	}
	return out, nil
}
