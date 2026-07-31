package api

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/Jemonee/simple-openai-gateway/pkg/common"
	"github.com/Jemonee/simple-openai-gateway/pkg/until"

	"github.com/gin-gonic/gin"
)

// IApi 业务 API 控制器接口，实现此接口在 Register 中注册自身路由
type IApi interface {
	Register(router *gin.RouterGroup)
}

// IRootApi registers protocol-compatible routes that live outside the internal /api namespace.
type IRootApi interface {
	RegisterRoot(router *gin.Engine)
}

// BaseApi API 层基类，业务 API 内嵌此结构体以复用通用能力。
type BaseApi struct{}

// DeferPanicHandler 统一 panic 恢复，将 ServicePanic/error 转为 R[T] 失败响应并写回客户端。
// 使用方式：在 handler 首行 defer a.DeferPanicHandler(c)
func (a *BaseApi) DeferPanicHandler(c *gin.Context) {
	if r := recover(); r != nil {
		stack := string(debug.Stack())
		var result common.R[any]
		switch err := r.(type) {
		case common.ServicePanic:
			until.Log.Errorf("请求发生业务 panic: method=%s path=%s code=%d msg=%s\n%s", c.Request.Method, c.Request.URL.Path, err.Code, err.Msg, stack)
			result = common.F[any](err.Code, err.Msg)
		case error:
			until.Log.Errorf("请求发生 panic: method=%s path=%s err=%v\n%s", c.Request.Method, c.Request.URL.Path, err, stack)
			result = common.F[any](500, "操作产生异常错误："+err.Error())
		default:
			until.Log.Errorf("请求发生未知 panic: method=%s path=%s panic=%v\n%s", c.Request.Method, c.Request.URL.Path, r, stack)
			result = common.F[any](500, "发生未知异常："+fmt.Sprintf("%v", r))
		}
		c.JSON(http.StatusOK, result)
	}
}
