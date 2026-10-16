package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestChatGPTOAuthBufferedResponsesRejectInvalidIndices(t *testing.T) {
	events := []struct {
		typeName string
		field    string
	}{
		{"response.output_item.added", "output_index"},
		{"response.output_item.done", "output_index"},
		{"response.content_part.added", "content_index"},
		{"response.content_part.done", "content_index"},
		{"response.output_text.delta", "content_index"},
		{"response.output_text.done", "content_index"},
		{"response.refusal.delta", "content_index"},
		{"response.refusal.done", "content_index"},
		{"response.reasoning_summary_part.added", "summary_index"},
		{"response.reasoning_summary_part.done", "summary_index"},
		{"response.reasoning_summary_text.delta", "summary_index"},
		{"response.reasoning_summary_text.done", "summary_index"},
		{"response.function_call_arguments.delta", "output_index"},
		{"response.function_call_arguments.done", "output_index"},
		{"response.custom_tool_call_input.delta", "output_index"},
		{"response.custom_tool_call_input.done", "output_index"},
	}
	for _, event := range events {
		for _, index := range []string{"-1", "4096", "1e100", "0.5", `"0"`, "null"} {
			t.Run(event.typeName+"/"+index, func(t *testing.T) {
				body := fmt.Sprintf("data: {\"type\":%q,%q:%s,\"delta\":\"hello\",\"item\":{\"type\":\"message\"},\"part\":{\"type\":\"output_text\"}}\n\n", event.typeName, event.field, index)
				body += "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_invalid_index\",\"status\":\"completed\",\"output\":[]}}\n\n"
				converted, adapted, err := adaptChatGPTOAuthBufferedResponse(http.Header{"Content-Type": []string{"text/event-stream"}}, []byte(body), "responses", "public-model")
				if err == nil || !strings.Contains(err.Error(), event.field) || !adapted || converted != nil {
					t.Fatalf("invalid %s=%s: adapted=%v, error=%v, response=%s", event.field, index, adapted, err, converted)
				}
			})
		}
	}
}

func TestChatGPTResponsesSSEToJSONPreservesSparseAndDefaultIndices(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":2,"delta":"third"}`,
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"first"}`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":2,"summary_index":1,"delta":"reason"}`,
		`data: {"type":"response.output_text.delta","delta":"default"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"final"}]}}`,
		`data: {"type":"response.output_text.delta","delta":"!"}`,
		`data: {"type":"response.completed","output_index":-1,"content_index":1e100,"response":{"id":"resp_indices","status":"completed","output":[]}}`,
	}, "\n\n") + "\n\n"
	converted, err := chatGPTResponsesSSEToJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []struct {
			Content []map[string]any `json:"content"`
			Summary []map[string]any `json:"summary"`
		} `json:"output"`
	}
	if err := json.Unmarshal(converted, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 3 || len(response.Output[0].Content) != 1 || response.Output[0].Content[0]["text"] != "final!" {
		t.Fatalf("default index output = %s", converted)
	}
	content := response.Output[1].Content
	if len(content) != 3 || content[0]["text"] != "first" || content[1] != nil || content[2]["text"] != "third" {
		t.Fatalf("sparse content = %s", converted)
	}
	summary := response.Output[2].Summary
	if len(summary) != 2 || summary[0] != nil || summary[1]["text"] != "reason" {
		t.Fatalf("sparse summary = %s", converted)
	}
}

func TestChatGPTResponsesSSEToJSONBoundsCumulativeSparseAllocation(t *testing.T) {
	for _, count := range []int{16, 17} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var body strings.Builder
			for index := 0; index < count; index++ {
				typeName, field := "response.output_text.delta", "content_index"
				if index%2 == 1 {
					typeName, field = "response.reasoning_summary_text.delta", "summary_index"
				}
				fmt.Fprintf(&body, "data: {\"type\":%q,\"output_index\":%d,%q:4095,\"delta\":\"text\"}\n\n", typeName, index, field)
			}
			// Updating existing content at the budget boundary must not charge more slots.
			body.WriteString("data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":4095,\"delta\":\"!\"}\n\n")
			body.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_sparse_budget\",\"status\":\"completed\",\"output\":[]}}\n\n")
			converted, err := chatGPTResponsesSSEToJSON([]byte(body.String()))
			if count == 17 {
				if err == nil || !strings.Contains(err.Error(), "indexed content exceeds size limit") || converted != nil {
					t.Fatalf("unbounded sparse response: error=%v, response bytes=%d", err, len(converted))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []struct {
					Content []map[string]any `json:"content"`
				} `json:"output"`
			}
			if err := json.Unmarshal(converted, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) != 16 || len(response.Output[0].Content) != 4096 || response.Output[0].Content[4095]["text"] != "text!" {
				t.Fatalf("valid sparse response lost output: %d output items", len(response.Output))
			}
		})
	}
}
