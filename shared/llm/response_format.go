package llm

// responseFormatBody translates the internal schema to the Chat Completions
// wire contract shared by OpenAI-compatible APIs and OpenRouter.
func responseFormatBody(format *ResponseFormat) map[string]any {
	body := map[string]any{"type": format.Type}
	if format.Type == "json_schema" {
		body["json_schema"] = map[string]any{
			"name":   "structured_output",
			"strict": true,
			"schema": format.Schema,
		}
	}
	return body
}
