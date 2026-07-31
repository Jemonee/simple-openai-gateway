package api

import (
	"github.com/Jemonee/simple-openai-gateway/internal/app"
	"github.com/Jemonee/simple-openai-gateway/internal/config"
	"github.com/Jemonee/simple-openai-gateway/internal/projectmeta"
	pkgApi "github.com/Jemonee/simple-openai-gateway/pkg/api"
	"github.com/Jemonee/simple-openai-gateway/pkg/common"
	"net/http"
	"time"

	gin "github.com/gin-gonic/gin"
)

type SiteApi struct {
	pkgApi.BaseApi
	configManager *config.ApplicationConfigManager
}

type SiteInfo struct {
	AppName     string `json:"appName"`
	Title       string `json:"title"`
	Version     string `json:"version"`
	BasePath    string `json:"basePath"`
	Host        string `json:"host"`
	Port        string `json:"port"`
	StartedAt   string `json:"startedAt"`
	RunningFor  string `json:"runningFor"`
	Description string `json:"description"`
}

func NewSiteApi(configManager *config.ApplicationConfigManager) *SiteApi {
	return &SiteApi{configManager: configManager}
}

func (a *SiteApi) Register(router *gin.RouterGroup) {
	router.GET("/site/info", a.GetSiteInfo)
}

func (a *SiteApi) GetSiteInfo(c *gin.Context) {
	defer a.DeferPanicHandler(c)

	cfg := a.configManager.GetConfig()
	data := SiteInfo{
		AppName:     projectmeta.AppName,
		Title:       projectmeta.DisplayName,
		Version:     projectmeta.Version,
		BasePath:    app.StaticBasePath,
		Host:        cfg.WebConfig.Host,
		Port:        cfg.WebConfig.Port,
		StartedAt:   app.StartedAt.Format(time.RFC3339),
		RunningFor:  time.Since(app.StartedAt).Round(time.Second).String(),
		Description: projectmeta.Description,
	}
	var result any = data
	c.JSON(http.StatusOK, common.S(&result))
}
