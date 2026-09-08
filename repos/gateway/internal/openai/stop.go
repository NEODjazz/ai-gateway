package openai

// StopSequences normalizes the chat stop union without changing delimiters.
func StopSequences(value any) ([]string, bool) {
	var sequences []string
	switch value := value.(type) {
	case nil:
		return nil, true
	case string:
		sequences = []string{value}
	case []string:
		sequences = value
	case []any:
		if len(value) > 4 {
			return nil, false
		}
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			sequences = append(sequences, text)
		}
	default:
		return nil, false
	}
	if len(sequences) > 4 {
		return nil, false
	}
	for _, text := range sequences {
		if text == "" {
			return nil, false
		}
	}
	return sequences, true
}
