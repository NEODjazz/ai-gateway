package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

const maxResponseJSONBytes = 32 << 20

func decodeResponseJSON(reader io.Reader) (openai.ResponseResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if len(payload) > maxResponseJSONBytes {
		return openai.ResponseResponse{}, errors.New("upstream Responses JSON exceeds 32 MiB")
	}
	var response *openai.ResponseResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if response == nil {
		return openai.ResponseResponse{}, errors.New("upstream Responses JSON must be an object")
	}
	if err := recordResponseInputUsage(payload, response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseUsage(response.Usage); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseOutputItems(response.Output); err != nil {
		return openai.ResponseResponse{}, err
	}
	response.OutputText = responseText(*response)
	return *response, nil
}

func validateResponseOutputItems(items []openai.ResponseOutputItem) error {
	for _, item := range items {
		switch item.Type {
		case "computer_call":
			if err := validateResponseComputerCall(item); err != nil {
				return err
			}
			continue
		case "shell_call":
			if err := validateResponseShellCall(item); err != nil {
				return err
			}
			continue
		case "shell_call_output":
			if _, message := openai.InspectResponseShellCallOutputs([]openai.ResponseOutputItem{item}); message != "" {
				return errors.New("provider " + message)
			}
			continue
		case "apply_patch_call":
			if message := openai.ValidateResponseApplyPatchCall(item); message != "" {
				return errors.New(message)
			}
			continue
		case "apply_patch_call_output":
			if _, message := openai.InspectResponseApplyPatchCallOutputs([]openai.ResponseOutputItem{item}); message != "" {
				return errors.New("provider " + message)
			}
			continue
		}
		if item.Type != "image_generation_call" {
			continue
		}
		if len(item.Result) == 0 {
			return errors.New("provider image generation call is missing result")
		}
		if string(item.Result) == "null" {
			continue
		}
		var encoded string
		if json.Unmarshal(item.Result, &encoded) != nil || encoded == "" {
			return errors.New("provider image generation result must be base64 or null")
		}
		decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded))
		if _, err := io.Copy(io.Discard, decoder); err != nil {
			return errors.New("provider image generation result is malformed base64")
		}
	}
	return nil
}

func validateResponseShellCall(item openai.ResponseOutputItem) error {
	if strings.TrimSpace(item.CallID) != item.CallID || item.CallID == "" || utf8.RuneCountInString(item.CallID) > 512 {
		return errors.New("provider shell call has an invalid call_id")
	}
	if item.ID != "" && (strings.TrimSpace(item.ID) != item.ID || utf8.RuneCountInString(item.ID) > 512) {
		return errors.New("provider shell call has an invalid id")
	}
	if item.CreatedBy != "" && (strings.TrimSpace(item.CreatedBy) != item.CreatedBy || utf8.RuneCountInString(item.CreatedBy) > 512) {
		return errors.New("provider shell call has an invalid created_by")
	}
	status, _ := json.Marshal(item.Status)
	if message := openai.ValidateResponseShellStatus(status, "provider shell call status"); message != "" {
		return errors.New(message)
	}
	if message := openai.ValidateResponseShellCallAction(item.Action); message != "" {
		return errors.New(message)
	}
	if len(item.Caller) > 0 && string(item.Caller) != "null" {
		if message := openai.ValidateResponseShellCaller(item.Caller, "provider shell call caller"); message != "" {
			return errors.New(message)
		}
	}
	if len(item.Environment) > 0 && string(item.Environment) != "null" {
		var environment any
		if json.Unmarshal(item.Environment, &environment) != nil {
			return errors.New("provider shell call has an invalid environment")
		}
		kind, _, _, message := openai.InspectResponseShellEnvironment(environment)
		if message != "" || kind == "container_auto" {
			return errors.New("provider shell call has an invalid environment")
		}
	}
	return nil
}

func validateResponseComputerCall(item openai.ResponseOutputItem) error {
	if strings.TrimSpace(item.CallID) != item.CallID || item.CallID == "" || utf8.RuneCountInString(item.CallID) > 512 {
		return errors.New("provider computer call has an invalid call_id")
	}
	if item.Status != "" && item.Status != "in_progress" && item.Status != "completed" && item.Status != "incomplete" {
		return errors.New("provider computer call has an invalid status")
	}
	hasAction := len(item.Action) != 0 && string(item.Action) != "null"
	hasActions := len(item.Actions) != 0
	if hasAction == hasActions {
		return errors.New("provider computer call requires exactly one of action or actions")
	}
	actions := item.Actions
	if hasAction {
		actions = []json.RawMessage{item.Action}
	}
	if len(actions) > 128 {
		return errors.New("provider computer call contains too many actions")
	}
	for _, action := range actions {
		if err := validateResponseComputerAction(action); err != nil {
			return err
		}
	}
	if len(item.PendingSafetyChecks) > 128 {
		return errors.New("provider computer call contains too many pending safety checks")
	}
	seen := make(map[string]struct{}, len(item.PendingSafetyChecks))
	for _, raw := range item.PendingSafetyChecks {
		var check map[string]json.RawMessage
		if json.Unmarshal(raw, &check) != nil || check == nil {
			return errors.New("provider computer call contains an invalid safety check")
		}
		var id string
		if json.Unmarshal(check["id"], &id) != nil || strings.TrimSpace(id) != id || id == "" || utf8.RuneCountInString(id) > 512 {
			return errors.New("provider computer call safety check has an invalid id")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("provider computer call contains duplicate safety checks")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateResponseComputerAction(raw json.RawMessage) error {
	var action map[string]json.RawMessage
	if json.Unmarshal(raw, &action) != nil || action == nil {
		return errors.New("provider computer call contains an invalid action")
	}
	var actionType string
	if json.Unmarshal(action["type"], &actionType) != nil {
		return errors.New("provider computer call action is missing type")
	}
	requireCoordinates := func(names ...string) bool {
		for _, name := range names {
			if !responseComputerInteger(action[name]) {
				return false
			}
		}
		return true
	}
	switch actionType {
	case "click", "double_click":
		if !requireCoordinates("x", "y") {
			return errors.New("provider computer pointer action has invalid coordinates")
		}
		var button string
		if json.Unmarshal(action["button"], &button) != nil || button != "left" && button != "right" && button != "wheel" && button != "back" && button != "forward" {
			return errors.New("provider computer pointer action has an invalid button")
		}
	case "move":
		if !requireCoordinates("x", "y") {
			return errors.New("provider computer pointer action has invalid coordinates")
		}
	case "scroll":
		if !requireCoordinates("x", "y", "scroll_x", "scroll_y") {
			return errors.New("provider computer scroll action has invalid coordinates")
		}
	case "drag":
		var path []map[string]json.RawMessage
		if json.Unmarshal(action["path"], &path) != nil || len(path) == 0 || len(path) > 1024 {
			return errors.New("provider computer drag action has an invalid path")
		}
		for _, point := range path {
			if !responseComputerInteger(point["x"]) || !responseComputerInteger(point["y"]) {
				return errors.New("provider computer drag action has invalid coordinates")
			}
		}
	case "keypress":
		var keys []string
		if json.Unmarshal(action["keys"], &keys) != nil || len(keys) == 0 || len(keys) > 32 {
			return errors.New("provider computer keypress action has invalid keys")
		}
		for _, key := range keys {
			if strings.TrimSpace(key) == "" || utf8.RuneCountInString(key) > 64 {
				return errors.New("provider computer keypress action has invalid keys")
			}
		}
	case "type":
		var text string
		if json.Unmarshal(action["text"], &text) != nil || len(text) > 1<<20 || !utf8.ValidString(text) {
			return errors.New("provider computer type action has invalid text")
		}
	case "wait", "screenshot":
	default:
		return errors.New("provider computer call contains an unsupported action type")
	}
	return nil
}

func responseComputerInteger(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	value := string(raw)
	if strings.ContainsAny(value, ".eE") {
		return false
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}
