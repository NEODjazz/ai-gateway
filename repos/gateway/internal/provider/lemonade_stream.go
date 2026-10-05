package provider

import (
	"encoding/json"
	"errors"
	"strconv"
)

func newLemonadeChatStreamNormalizer() func(string) (string, error) {
	// Some native backends timestamp each chunk when it is emitted. The public
	// completion timestamp is the first reported timestamp for this request.
	var firstCreated *int64
	return func(payload string) (string, error) {
		if err := validateLemonadeTokenUsage([]byte(payload), false); err != nil {
			return "", err
		}
		var chunk struct {
			Created *int64 `json:"created"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return "", err
		}
		if chunk.Created == nil {
			return payload, nil
		}
		if *chunk.Created < 0 {
			return "", errors.New("provider returned invalid chat completion timestamp")
		}
		if firstCreated == nil {
			firstCreated = chunk.Created
		}
		if *chunk.Created == *firstCreated {
			return payload, nil
		}
		// RawMessage preserves integer precision, tool arguments and extension
		// fields while rewriting only the timestamp, before validation/delivery.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &fields); err != nil {
			return "", err
		}
		fields["created"] = json.RawMessage(strconv.FormatInt(*firstCreated, 10))
		normalized, err := json.Marshal(fields)
		return string(normalized), err
	}
}
