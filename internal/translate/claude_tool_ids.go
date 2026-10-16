package translate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const claudeToolIDPrefix = "cpa_tool_v1_"
const bridgeToolIDPrefix = "cpa_tool_"

var errInvalidClaudeToolIDCarrier = errors.New("invalid bridge tool ID carrier")

type claudeToolIDCarrier struct {
	ItemID string `json:"i,omitempty"`
	CallID string `json:"c"`
}

func encodeClaudeToolID(itemID, callID string) string {
	if itemID == "" && callID == "" {
		return ""
	}
	if itemID != "" && itemID == callID && safeClaudeToolID(callID) && !strings.HasPrefix(callID, bridgeToolIDPrefix) {
		return callID
	}
	encoded, err := json.Marshal(claudeToolIDCarrier{ItemID: itemID, CallID: callID})
	if err != nil {
		return callID
	}
	return claudeToolIDPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeClaudeToolID(value string) (itemID, callID string, ok bool) {
	itemID, callID, encoded, err := parseClaudeToolID(value)
	return itemID, callID, encoded && err == nil
}

func parseClaudeToolID(value string) (itemID, callID string, encoded bool, err error) {
	if !strings.HasPrefix(value, bridgeToolIDPrefix) {
		return "", value, false, nil
	}
	if !strings.HasPrefix(value, claudeToolIDPrefix) {
		return "", "", false, errInvalidClaudeToolIDCarrier
	}
	encodedValue := strings.TrimPrefix(value, claudeToolIDPrefix)
	decoded, errDecode := base64.RawURLEncoding.DecodeString(encodedValue)
	if errDecode != nil {
		return "", "", false, errInvalidClaudeToolIDCarrier
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var carrier claudeToolIDCarrier
	if decoder.Decode(&carrier) != nil || decoder.Decode(new(any)) != io.EOF || (carrier.CallID == "" && carrier.ItemID == "") {
		return "", "", false, errInvalidClaudeToolIDCarrier
	}
	canonical, errMarshal := json.Marshal(carrier)
	if errMarshal != nil || !bytes.Equal(decoded, canonical) || base64.RawURLEncoding.EncodeToString(canonical) != encodedValue {
		return "", "", false, errInvalidClaudeToolIDCarrier
	}
	return carrier.ItemID, carrier.CallID, true, nil
}

// DecodeClaudeToolIDs returns the Responses IDs encoded in a Claude tool-use ID.
// The ok result is false when value is an ordinary, unencoded Claude tool ID.
func DecodeClaudeToolIDs(value string) (itemID, callID string, ok bool) {
	return decodeClaudeToolID(value)
}

func safeClaudeToolID(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func claudeToolIDFromResponses(item map[string]any) string {
	return encodeClaudeToolID(rawStringValue(item["id"]), rawStringValue(item["call_id"]))
}

func responsesToolIDsFromClaude(value string) (itemID, callID string, err error) {
	itemID, callID, encoded, err := parseClaudeToolID(value)
	if err != nil || encoded {
		return itemID, callID, err
	}
	return "", value, nil
}
