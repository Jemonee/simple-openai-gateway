package app

import (
	"embed"
	"io/fs"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
	pkgApi "github.com/Jemonee/simple-openai-gateway/pkg/api"
	"github.com/Jemonee/simple-openai-gateway/pkg/until"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

var log = until.Log

var assetExtensions = map[string]bool{
	".js":   true,
	".css":  true,
	".gif":  true,
	".svg":  true,
	".ico":  true,
	".ttf":  true,
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".woff": true,
	".json": true,
	".webp": true,
}

var extContentTypeMap = map[string]string{
	".html":  "text/html; charset=utf-8",
	".htm":   "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "application/javascript", // 重要：使用 application/javascript
	".mjs":   "application/javascript", // 重要：模块 JavaScript
	".json":  "application/json",
	".xml":   "application/xml",
	".txt":   "text/plain",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".ico":   "image/x-icon",
	".svg":   "image/svg+xml",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".eot":   "application/vnd.ms-fontobject",
	".otf":   "font/otf",
	".mp3":   "audio/mpeg",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
	".pdf":   "application/pdf",
	".zip":   "application/zip",
	".wasm":  "application/wasm",
}

type ApplicationHolder struct {
	AppWebManager    *AppWebManager
	AppConfigManager *config.ApplicationConfigManager
}

func NewApplicationHolder(appConfigManager *config.ApplicationConfigManager, appWebManager *AppWebManager) *ApplicationHolder {
	return &ApplicationHolder{
		AppConfigManager: appConfigManager,
		AppWebManager:    appWebManager,
	}
}

func (applicationHolder *ApplicationHolder) Start(staticFS embed.FS) {
	applicationHolder.AppWebManager.Run(staticFS)
}

type AppWebManager struct {
	WebServer       *gin.Engine
	WebConfig       *config.WebConfig
	assertFs        embed.FS
	staticFiles     map[string]bool
	apis            []pkgApi.IApi
	rootApi         pkgApi.IRootApi
	browserOpenOnce sync.Once
	browserOpener   func(string) error
	openBrowser     bool
}

func NewAppWebManager(appConfigManager *config.ApplicationConfigManager, apis []pkgApi.IApi, rootApi pkgApi.IRootApi) *AppWebManager {
	var webConfig *config.WebConfig = nil
	appConfig := appConfigManager.GetConfig()
	if appConfig != nil {
		webConfig = &appConfig.WebConfig
	}
	openBrowser, err := config.OpenBrowserOnStart()
	if err != nil {
		panic(err)
	}
	return &AppWebManager{
		WebServer:     getGin(),
		WebConfig:     webConfig,
		apis:          apis,
		rootApi:       rootApi,
		browserOpener: openDefaultBrowser,
		openBrowser:   openBrowser,
	}
}

func getGin() *gin.Engine {
	engine := gin.Default()
	if err := engine.SetTrustedProxies(nil); err != nil {
		panic(err)
	}
	// 重定向 Gin 日志
	gin.DefaultWriter = &until.GinLogWriter{}
	gin.DefaultErrorWriter = &until.GinLogWriter{}
	// 更换默认的日志输出方式
	engine.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		log.WithFields(logrus.Fields{
			"client_ip":  param.ClientIP,
			"method":     param.Method,
			"path":       param.Path,
			"status":     param.StatusCode,
			"latency":    param.Latency,
			"user_agent": param.Request.UserAgent(),
		}).Info("HTTP Request")

		return ""
	}))
	return engine
}

func (awm *AppWebManager) Run(assertFs embed.FS) {
	awm.assertFs = assertFs
	webConfig := awm.WebConfig
	awm.RegisterRouter()
	if webConfig == nil {
		webConfig = &config.WebConfig{
			Port: config.DefaultWebPort,
			Host: config.DefaultWebHost,
		}
	}
	listenAddress := net.JoinHostPort(webConfig.Host, webConfig.Port)
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		panic(err)
	}
	localURL := localBrowserURL(webConfig.Host, webConfig.Port)
	go func() {
		if err := awm.WebServer.RunListener(listener); err != nil {
			panic(err)
		}
	}()
	log.Infof("application listening on %s", listenAddress)
	log.Infof("local interface available at %s", localURL)
	if awm.openBrowser {
		go awm.openBrowserOnce(localURL)
	} else {
		log.Infof("automatic browser opening is disabled by %s", config.OpenBrowserOnStartEnvKey)
	}
}

func (awm *AppWebManager) RegisterRouter() {
	server := awm.WebServer
	awm.RegisterStatic(server.Group("/static"))

	apiGroup := server.Group("/api")
	for _, a := range awm.apis {
		a.Register(apiGroup)
	}
	if awm.rootApi != nil {
		awm.rootApi.RegisterRoot(server)
	}

	server.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/static/")
	})
}

func getMimeType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if mime, ok := extContentTypeMap[ext]; ok {
		return mime
	}

	return "application/octet-stream"
}

func (awm *AppWebManager) RegisterStatic(router *gin.RouterGroup) {
	webRootDir, err := fs.Sub(awm.assertFs, path.Join("frontend", "dist"))
	if err != nil {
		panic(err)
	}
	awm.staticFiles = map[string]bool{}
	// 预生成静态资源映射
	err = fs.WalkDir(webRootDir, ".", func(filePath string, d fs.DirEntry, err error) error {
		if !d.IsDir() {
			filePath := filepath.ToSlash(filePath)
			log.Info("前端资源：" + filePath)
			awm.staticFiles[filePath] = true
		}
		return nil
	})
	if err != nil {
		log.Error("register static files error: ", err)
	}

	// 处理前端资源
	router.GET("/*filepath", func(c *gin.Context) {
		reqPath := c.Param("filepath")
		resourcePath := strings.TrimPrefix(filepath.ToSlash(reqPath), "/")
		if resourcePath == "" {
			resourcePath = "index.html"
		}
		//resourcePath = filepath.Clean(resourcePath)
		requestExt := filepath.Ext(resourcePath)
		log.Info("前端访问资源：" + resourcePath)
		// 如果在当前目录中能够直接找到 就直接返回对应文件
		if _, ok := awm.staticFiles[resourcePath]; ok {
			serveEmbeddedFile(c, webRootDir, resourcePath)
			return
		}
		// 如果访问的是静态文件就返回 404
		if _, ok := assetExtensions[requestExt]; ok {
			c.Status(http.StatusNotFound)
			return
		}
		serveEmbeddedFile(c, webRootDir, "index.html")
	})
}

func serveEmbeddedFile(c *gin.Context, assertFs fs.FS, filePath string) {
	f, err := fs.ReadFile(assertFs, filePath)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// 手动设置内容类型
	contentType := getMimeType(filePath)
	if contentType != "" {
		c.Header("Content-Type", contentType)
	}

	// 发送内容
	c.Data(http.StatusOK, contentType, f)
}
