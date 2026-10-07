package proxy

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"gpt-load/internal/utils"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// removeHopByHopHeaders 传输层头只属于当前连接, 不能直接复制到下一跳.
func removeHopByHopHeaders(header http.Header) {
	for _, connection := range header.Values("Connection") {
		for _, name := range strings.Split(connection, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
}

// handlePassthroughResponse 保留应用层响应及多值头, 不根据 AI 请求字段重建响应.
func (ps *ProxyServer) handlePassthroughResponse(c *gin.Context, resp *http.Response) {
	headers := resp.Header.Clone()
	removeHopByHopHeaders(headers)
	for name, values := range headers {
		c.Writer.Header()[name] = append([]string(nil), values...)
	}
	c.Status(resp.StatusCode)
	if _, err := io.Copy(flushingResponseWriter{writer: c.Writer}, resp.Body); err != nil {
		logUpstreamError("copying passthrough response", err)
	}
}

type flushingResponseWriter struct {
	writer gin.ResponseWriter
}

func (w flushingResponseWriter) Write(data []byte) (int, error) {
	count, err := w.writer.Write(data)
	if count > 0 {
		w.writer.Flush()
	}
	return count, err
}

// redactPassthroughError 无密钥回显时保留原文, 无法可靠解码时返回安全错误文本.
func redactPassthroughError(resp *http.Response, body []byte, secret string) []byte {
	if secret == "" {
		return body
	}
	for name, values := range resp.Header {
		// 协议元数据不能按短密钥替换, 业务头仍需防止凭据回显.
		switch http.CanonicalHeaderKey(name) {
		case "Content-Type", "Content-Length", "Content-Encoding", "Content-Range", "Accept-Ranges",
			"Transfer-Encoding", "Connection", "Proxy-Connection", "Keep-Alive", "Te", "Trailer", "Upgrade",
			"Cache-Control", "Date", "Expires", "Last-Modified", "Vary", "Allow", "Retry-After":
			continue
		}
		for index, value := range values {
			values[index] = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
		resp.Header[name] = values
	}
	decoded, err := decodePassthroughErrorBody(resp.Header, body)
	var redacted []byte
	if err != nil {
		// 不返回原压缩数据或解码器错误, 避免无法检查的内容泄漏密钥.
		redacted = []byte(strings.ReplaceAll("Upstream error response could not be safely decoded.", secret, "[REDACTED]"))
		resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		redacted = bytes.ReplaceAll(decoded, []byte(secret), []byte("[REDACTED]"))
		if bytes.Equal(decoded, redacted) {
			return body
		}
	}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	return redacted
}

// decodePassthroughErrorBody 合并多值编码头并倒序解码, 不使用会吞掉错误的全局入口.
func decodePassthroughErrorBody(header http.Header, body []byte) ([]byte, error) {
	values := header.Values("Content-Encoding")
	if len(values) == 0 {
		return body, nil
	}
	encodings := strings.Split(strings.Join(values, ","), ",")
	for index := len(encodings) - 1; index >= 0; index-- {
		var decompressor utils.Decompressor
		switch strings.ToLower(strings.TrimSpace(encodings[index])) {
		case "identity":
			continue
		case "gzip":
			decompressor = &utils.GzipDecompressor{}
		case "br":
			decompressor = &utils.BrotliDecompressor{}
		case "zstd":
			decompressor = &utils.ZstdDecompressor{}
		case "deflate":
			decoded, err := decodePassthroughDeflate(body)
			if err != nil {
				return nil, err
			}
			body = decoded
			continue
		default:
			return nil, errors.New("unsupported upstream content encoding")
		}
		if len(body) == 0 {
			return nil, io.ErrUnexpectedEOF
		}
		decoded, err := decompressor.Decompress(body)
		if err != nil {
			return nil, err
		}
		body = decoded
	}
	return body, nil
}

// decodePassthroughDeflate 优先读取标准 zlib, 仅头不匹配时兼容 raw deflate.
func decodePassthroughDeflate(body []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(body))
	if err == zlib.ErrHeader {
		reader = flate.NewReader(bytes.NewReader(body))
	} else if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
