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

func newLemonadeResponseStreamNormalizer() func(string) (string, error) {
	// Some native backends omit output_index. Resolve it from stable item IDs
	// established by output_item.added, without relaxing generic validation.
	indices := make(map[string]int)
	owners := make(map[int]string)
	return func(payload string) (string, error) {
		var event struct {
			Type   string `json:"type"`
			ItemID string `json:"item_id"`
			Item   struct {
				ID string `json:"id"`
			} `json:"item"`
			Index json.RawMessage `json:"output_index"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return "", err
		}
		id := event.ItemID
		if event.Type == "response.output_item.added" || event.Type == "response.output_item.done" {
			if id != "" && event.Item.ID != "" && id != event.Item.ID {
				return "", errors.New("Lemonade returned contradictory output item IDs")
			}
			if id == "" {
				id = event.Item.ID
			}
		}
		if id == "" {
			if len(event.Index) == 0 && (event.Type == "response.output_item.added" || event.Type == "response.output_item.done") {
				return "", errors.New("Lemonade omitted both output item ID and output_index")
			}
			return payload, nil
		}
		if !validResponseResourceID(id) {
			return "", errors.New("Lemonade returned an invalid output item ID")
		}
		index, known := indices[id]
		explicit := len(event.Index) > 0
		if explicit {
			var reported int
			if string(event.Index) == "null" || json.Unmarshal(event.Index, &reported) != nil || reported < 0 || reported >= maxResponseStreamOutputItems {
				return "", errors.New("Lemonade returned an invalid output_index")
			}
			if known && reported != index {
				return "", errors.New("Lemonade changed an output item's index")
			}
			index = reported
		}
		if !known {
			if event.Type != "response.output_item.added" {
				if explicit {
					return payload, nil
				}
				return "", errors.New("Lemonade omitted output_index for an unknown output item")
			}
			if !explicit {
				index = 0
				for owners[index] != "" {
					index++
				}
			}
			if len(indices) >= maxResponseStreamOutputItems || index >= maxResponseStreamOutputItems {
				return "", errors.New("Lemonade returned too many output items")
			}
			if owner := owners[index]; owner != "" && owner != id {
				return "", errors.New("Lemonade assigned conflicting output item indices")
			}
			indices[id], owners[index] = index, id
		}
		if explicit {
			return payload, nil
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &fields); err != nil {
			return "", err
		}
		fields["output_index"] = json.RawMessage(strconv.Itoa(index))
		normalized, err := json.Marshal(fields)
		return string(normalized), err
	}
}
