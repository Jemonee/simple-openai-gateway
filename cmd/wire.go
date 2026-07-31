//go:build wireinject
// +build wireinject

// The build tag makes sure the stub is not built in the final build.
package cmd

import (
	"github.com/Jemonee/simple-openai-gateway/internal/api"
	apiProviders "github.com/Jemonee/simple-openai-gateway/internal/api/providers"
	"github.com/Jemonee/simple-openai-gateway/internal/app"
	"github.com/Jemonee/simple-openai-gateway/internal/config"
	dsProviders "github.com/Jemonee/simple-openai-gateway/internal/core/ds/providers"
	"github.com/Jemonee/simple-openai-gateway/internal/gateway"

	"github.com/google/wire"
)

func InitializeApp() *app.ApplicationHolder {
	wire.Build(
		// 配置
		config.NewApplicationConfigManager,
		dsProviders.NewMultiDataSource,
		dsProviders.GetPrimaryDataSource,
		gateway.NewStore,
		gateway.NewAdminAuthService,
		gateway.NewManagementService,
		gateway.NewClientAccessService,
		gateway.NewTokenEstimator,
		gateway.NewRouter,
		gateway.NewRelayService,

		// API
		api.NewSiteApi,
		api.NewAdminSecurity,
		api.NewAdminApi,
		api.NewGatewayManagementApi,
		api.NewOpenAIRelayApi,
		api.NewSettingsApi,
		apiProviders.ProvideApis,
		apiProviders.ProvideRootApi,

		// 应用层
		app.NewAppWebManager,
		app.NewApplicationHolder,
	)
	return nil
}
