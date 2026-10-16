package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// OpenAI 将 usage 放在所有 choices 的 finish_reason 之后，不能提前关闭响应体。
func TestChatStreamReadsUsageAfterFinishedChoices(t *testing.T) {
	for _, ending := range []string{"done", "eof"} {
		for _, finishReason := range []string{"stop", "tool_calls"} {
			t.Run(ending+"/"+finishReason, func(t *testing.T) {
				store := newTestStore(t)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request map[string]any
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					options, _ := request["stream_options"].(map[string]any)
					if options["include_usage"] != true {
						t.Error("upstream request must include stream usage")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for index := 0; index < 2; index++ {
						fmt.Fprintf(w, "data: {\"id\":\"chat-cache\",\"choices\":[{\"index\":%d,\"delta\":{\"content\":\"done\"},\"finish_reason\":%q}]}\n\n", index, finishReason)
						w.(http.Flusher).Flush()
					}
					fmt.Fprint(w, "data: {\"id\":\"chat-cache\",\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":80},\"cost\":0.000042}}\n\n")
					if ending == "done" {
						fmt.Fprint(w, "data: [DONE]\n\n")
						w.(http.Flusher).Flush()
						// [DONE] 后连接保持打开，网关仍须立即结束。
						<-r.Context().Done()
					}
				}))
				defer upstream.Close()
				token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
				body := []byte(`{"model":"public-model","stream":true,"n":2,"messages":[{"role":"user","content":"hello"}]}`)
				payload, err := ParseRelayPayload(body)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				recorder := httptest.NewRecorder()
				if publicErr := newTestRelay(store).Relay(ctx, recorder, nil, "", "chat", token, payload, body); publicErr != nil {
					t.Fatal(publicErr)
				}
				if ctx.Err() != nil {
					t.Fatalf("relay waited for upstream close: %v", ctx.Err())
				}
				if !strings.Contains(recorder.Body.String(), `"cached_tokens":80`) || ending == "done" && !strings.Contains(recorder.Body.String(), "[DONE]") {
					t.Fatalf("usage/end marker not forwarded: %s", recorder.Body.String())
				}
				var requestLog RelayRequestLog
				if err := store.db.First(&requestLog).Error; err != nil {
					t.Fatal(err)
				}
				if requestLog.Outcome != RelayOutcomeSuccess || requestLog.UsageSource != "upstream" || requestLog.InputTokens != 100 || requestLog.CachedTokens != 80 || requestLog.NormalInputTokens != 20 || requestLog.OutputTokens != 20 || requestLog.UpstreamCost != 42 {
					t.Fatalf("request usage = %+v", requestLog)
				}
				attempts := relayAttempts(t, store)
				if len(attempts) != 1 || attempts[0].UsageSource != "upstream" || attempts[0].CachedTokens != 80 || attempts[0].UpstreamCost != 42 {
					t.Fatalf("attempt usage = %+v", attempts)
				}
				var stat TokenDailyStat
				if err := store.db.First(&stat).Error; err != nil {
					t.Fatal(err)
				}
				if stat.CachedTokens != 80 || stat.InputTokens != 100 || stat.NormalInputTokens != 20 {
					t.Fatalf("daily usage = %+v", stat)
				}
			})
		}
	}
}
