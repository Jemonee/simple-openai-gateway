package api

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOpenAIRelayAPIRegistersImagesRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	(&OpenAIRelayApi{}).RegisterRoot(router)

	routes := make(map[string]bool)
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /v1/images/generations",
		"POST /v1/images/edits",
		"POST /v1/images/variations",
	} {
		if !routes[route] {
			t.Fatalf("route %s was not registered", route)
		}
	}
}
