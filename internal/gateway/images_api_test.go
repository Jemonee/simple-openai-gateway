package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type imageMultipartField struct {
	name  string
	value string
}

func imageMultipartBody(t *testing.T, fields []imageMultipartField, fileField string, fileData []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			t.Fatal(err)
		}
	}
	if fileField != "" {
		part, err := writer.CreateFormFile(fileField, "source.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(fileData); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func TestMultipartImagePayloadRewritesModelAndPreservesFile(t *testing.T) {
	fileData := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
	body, contentType := imageMultipartBody(t, []imageMultipartField{
		{name: "model", value: "public-image-model"},
		{name: "prompt", value: "replace the sky"},
		{name: "stream", value: "true"},
	}, "image[]", fileData)

	payload, err := ParseRelayPayloadWithContentType(body, contentType)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Model != "public-image-model" || !payload.Stream || !payload.IsMultipart() {
		t.Fatalf("parsed payload = model %q stream %t multipart %t", payload.Model, payload.Stream, payload.IsMultipart())
	}

	upstreamBody, upstreamContentType, err := payload.UpstreamRequestBody("upstream-image-model", "images/edits", false)
	if err != nil {
		t.Fatal(err)
	}
	if upstreamContentType != contentType {
		t.Fatalf("content type = %q, want %q", upstreamContentType, contentType)
	}
	mediaType, parameters, err := mime.ParseMediaType(upstreamContentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("upstream content type = %q, %v", upstreamContentType, err)
	}

	reader := multipart.NewReader(bytes.NewReader(upstreamBody), parameters["boundary"])
	values := make(map[string]string)
	var uploaded []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		if part.FileName() != "" {
			uploaded = data
		} else {
			values[part.FormName()] = string(data)
		}
	}
	if values["model"] != "upstream-image-model" || values["prompt"] != "replace the sky" || values["stream"] != "true" {
		t.Fatalf("upstream form values = %#v", values)
	}
	if !bytes.Equal(uploaded, fileData) {
		t.Fatalf("uploaded file = %v, want %v", uploaded, fileData)
	}
}

func TestImagePayloadValidation(t *testing.T) {
	if _, err := ParseRelayPayloadWithContentType([]byte("not multipart"), "multipart/form-data"); err == nil {
		t.Fatal("missing multipart boundary should fail")
	}
	body, contentType := imageMultipartBody(t, []imageMultipartField{{name: "prompt", value: "hello"}}, "", nil)
	if _, err := ParseRelayPayloadWithContentType(body, contentType); err == nil || err.Error() != "model is required" {
		t.Fatalf("missing model error = %v", err)
	}
}

func TestImageEndpointPathsAndResponses(t *testing.T) {
	for _, endpoint := range []string{"images/generations", "images/edits", "images/variations"} {
		if path := endpointPath(endpoint); path != endpoint {
			t.Fatalf("endpointPath(%q) = %q", endpoint, path)
		}
	}
	if failure := validateBufferedApplicationResponse("images/generations", []byte(`{"created":1,"data":[{"b64_json":"aW1hZ2U="}]}`)); failure != nil {
		t.Fatalf("valid image response failed: %v", failure)
	}
	if failure := validateBufferedApplicationResponse("images/edits", []byte(`{"created":1,"data":[]}`)); failure == nil || applicationFailureCode(failure) != "upstream_empty_response" {
		t.Fatalf("empty image response failure = %v", failure)
	}
	partial := []byte("data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"aW1hZ2U=\"}\n\n")
	if !sseEventHasUsableImageOutput(partial) {
		t.Fatal("partial image event was not recognized as usable output")
	}
	completed := []byte("data: {\"type\":\"image_generation.completed\"}\n\n")
	if !sseEventIsTerminalSuccess(completed, "images/generations") {
		t.Fatal("completed image event was not recognized as terminal")
	}
}

func TestRelayImagesEndpointsUseExistingRoutingPipeline(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		multipart bool
	}{
		{name: "generations", endpoint: "images/generations"},
		{name: "edits", endpoint: "images/edits", multipart: true},
		{name: "variations", endpoint: "images/variations", multipart: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fileData := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1/"+test.endpoint {
					t.Errorf("upstream path = %q", request.URL.Path)
				}
				if request.URL.RawQuery != "trace=image" {
					t.Errorf("upstream query = %q", request.URL.RawQuery)
				}
				if request.Header.Get("Authorization") != "Bearer upstream-secret" {
					t.Errorf("upstream authorization = %q", request.Header.Get("Authorization"))
				}
				if test.multipart {
					if err := request.ParseMultipartForm(1 << 20); err != nil {
						t.Errorf("parse upstream multipart: %v", err)
					} else {
						defer request.MultipartForm.RemoveAll()
						if request.FormValue("model") != "upstream-1" {
							t.Errorf("upstream model = %q", request.FormValue("model"))
						}
						file, _, err := request.FormFile("image")
						if err != nil {
							t.Errorf("upstream image: %v", err)
						} else {
							defer file.Close()
							actual, _ := io.ReadAll(file)
							if !bytes.Equal(actual, fileData) {
								t.Errorf("upstream image = %v", actual)
							}
						}
					}
				} else {
					var input map[string]any
					if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
						t.Errorf("decode upstream JSON: %v", err)
					}
					if input["model"] != "upstream-1" || input["prompt"] != "draw a lighthouse" {
						t.Errorf("upstream JSON = %#v", input)
					}
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"created":1,"data":[{"b64_json":"aW1hZ2U="}],"usage":{"input_tokens":3,"output_tokens":4}}`))
			}))
			defer upstream.Close()

			store := newTestStore(t)
			token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
			relay := newTestRelay(store)
			var body []byte
			contentType := "application/json"
			if test.multipart {
				body, contentType = imageMultipartBody(t, []imageMultipartField{
					{name: "model", value: "public-model"},
					{name: "prompt", value: "edit the image"},
				}, "image", fileData)
			} else {
				body = []byte(`{"model":"public-model","prompt":"draw a lighthouse","size":"1024x1024"}`)
			}
			payload, err := ParseRelayPayloadWithContentType(body, contentType)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			headers := make(http.Header)
			headers.Set("Content-Type", contentType)
			if publicErr := relay.Relay(context.Background(), recorder, headers, "trace=image", test.endpoint, token, payload, body); publicErr != nil {
				t.Fatal(publicErr)
			}
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "aW1hZ2U=") {
				t.Fatalf("downstream response = %d %s", recorder.Code, recorder.Body.String())
			}
			var log RelayRequestLog
			if err := store.db.Order("created_at desc").First(&log).Error; err != nil {
				t.Fatal(err)
			}
			if log.Endpoint != test.endpoint || log.RequestedModel != "public-model" || log.Outcome != RelayOutcomeSuccess {
				t.Fatalf("request log = endpoint %q model %q outcome %q", log.Endpoint, log.RequestedModel, log.Outcome)
			}
		})
	}
}
