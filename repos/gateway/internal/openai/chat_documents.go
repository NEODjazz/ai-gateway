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
			if ok && (object["type"] == "input_file_reference" || object["type"] == "input_file_image_reference" || object["type"] == "input_file_audio_reference" || object["type"] == "input_file_video_reference") {
				return true
			}
		}
	}
	return false
}

func HasChatURLReferences(request ChatCompletionRequest) bool {
	for _, message := range request.Messages {
		parts, ok := message.Content.([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			object, ok := part.(map[string]any)
			if ok && (object["type"] == "input_url_document" || object["type"] == "input_url_image") {
				return true
			}
		}
	}
	return false
}

func HasChatResolvableReferences(request ChatCompletionRequest) bool {
	return HasChatFileReferences(request) || HasChatURLReferences(request)
}
