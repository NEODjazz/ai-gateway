package openai

func HasChatTextDocuments(request ChatCompletionRequest) bool {
	for _, message := range request.Messages {
		parts, ok := message.Content.([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			object, ok := part.(map[string]any)
			if ok && object["type"] == "input_document" {
				return true
			}
		}
	}
	return false
}

func HasChatFileReferences(request ChatCompletionRequest) bool {
	for _, message := range request.Messages {
		parts, ok := message.Content.([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			object, ok := part.(map[string]any)
			if ok && object["type"] == "input_file_reference" {
				return true
			}
		}
	}
	return false
}
