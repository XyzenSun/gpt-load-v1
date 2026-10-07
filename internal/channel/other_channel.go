package channel

import (
	"context"
	"fmt"
	"gpt-load/internal/models"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

func init() {
	Register("other", newOtherChannel)
}

// OtherChannel 复用上游和密钥轮询, 将协议无关的请求修改留给显式配置.
type OtherChannel struct {
	*BaseChannel
}

func newOtherChannel(factory *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := factory.newBaseChannel("other", group)
	if err != nil {
		return nil, err
	}
	return &OtherChannel{BaseChannel: base}, nil
}

func (ch *OtherChannel) IsPassthrough() bool {
	return true
}

func (ch *OtherChannel) BuildUpstreamURL(originalURL *url.URL, groupName string) (string, error) {
	base := ch.getUpstreamURL()
	if base == nil {
		return "", fmt.Errorf("no upstream URL configured for channel %s", ch.Name)
	}

	finalURL := *base
	proxyPrefix := "/proxy/" + groupName
	// 先拼接编码路径再生成 Path, 避免编码尾斜杠使两者不一致而丢失 RawPath.
	escapedPrefix := (&url.URL{Path: proxyPrefix}).EscapedPath()
	escapedRequestPath := strings.TrimPrefix(originalURL.EscapedPath(), escapedPrefix)
	finalURL.RawPath = strings.TrimRight(base.EscapedPath(), "/") + escapedRequestPath
	decodedPath, err := url.PathUnescape(finalURL.RawPath)
	if err != nil {
		return "", fmt.Errorf("invalid escaped upstream path: %w", err)
	}
	finalURL.Path = decodedPath
	finalURL.RawQuery = originalURL.RawQuery
	finalURL.ForceQuery = originalURL.ForceQuery
	return finalURL.String(), nil
}

func (ch *OtherChannel) ModifyRequest(_ *http.Request, _ *models.APIKey, _ *models.Group) {
	// 默认不注入鉴权, 自定义请求头规则可以使用本次轮询到的密钥.
}

func (ch *OtherChannel) IsStreamRequest(_ *gin.Context, _ []byte) bool {
	// 通用响应由代理增量回传, 不通过请求字段推断 SSE, 并保留普通请求超时.
	return false
}

func (ch *OtherChannel) ExtractModel(_ *gin.Context, _ []byte) string {
	return ""
}

func (ch *OtherChannel) ValidateKey(_ context.Context, _ *models.APIKey, _ *models.Group) (bool, error) {
	// 返回值表示跳过校验, KeyValidator 同时跳过密钥状态更新.
	return true, nil
}

func (ch *OtherChannel) ApplyModelRedirect(_ *http.Request, bodyBytes []byte, _ *models.Group) ([]byte, error) {
	return bodyBytes, nil
}

func (ch *OtherChannel) TransformModelList(_ *http.Request, _ []byte, _ *models.Group) (map[string]any, error) {
	// 代理不会为通用渠道进入模型列表转换, 避免误调用造成响应语义改变.
	return nil, fmt.Errorf("model list transformation is not supported for other channels")
}
