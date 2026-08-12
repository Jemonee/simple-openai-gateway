package providers

import (
	"github.com/Jemonee/simple-openai-gateway/internal/api"
	pkgApi "github.com/Jemonee/simple-openai-gateway/pkg/api"
)

func ProvideApis(siteApi *api.SiteApi, settingsApi *api.SettingsApi, adminApi *api.AdminApi, gatewayApi *api.GatewayManagementApi, webChatApi *api.WebChatApi) []pkgApi.IApi {
	return []pkgApi.IApi{siteApi, settingsApi, adminApi, gatewayApi, webChatApi}
}

func ProvideRootApi(relayApi *api.OpenAIRelayApi) pkgApi.IRootApi {
	return relayApi
}
