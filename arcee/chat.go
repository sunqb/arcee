package arcee

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type CreateChatRequest struct {
	Message            string   `json:"message"`
	Title              string   `json:"title"`
	BaseModelName      string   `json:"base_model_name"`
	ChatID             string   `json:"chat_id,omitempty"`
	EnabledTools       []string `json:"enabledTools"`
	FileReferences     []any    `json:"fileReferences"`
	Temperature        float64  `json:"temperature"`
	ProviderPreference any      `json:"provider_preference"`
}

type StreamInit struct {
	AssistantMessageID string `json:"assistant_message_id"`
}

type StreamMetadata struct {
	ChatID             string `json:"chat_id"`
	UserMessageID      string `json:"user_message_id"`
	AssistantMessageID string `json:"assistant_message_id"`
	BaseModelName      string `json:"base_model_name"`
}

type CreateChatResult struct {
	Init     StreamInit
	Content  string
	Metadata StreamMetadata
	Raw      string
}

const (
	streamInitStart = "__STREAM_INIT__"
	streamInitEnd   = "__STREAM_INIT_END__"
	metadataStart   = "__METADATA__"
	metadataEnd     = "__METADATA_END__"
)

func (c *Client) CreateChat(ctx context.Context, accessToken string, reqBody CreateChatRequest) (*CreateChatResult, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required")
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal create-chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/completions/create-chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create create-chat request: %w", err)
	}

	if err := setBrowserHeaders(httpReq); err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/plain")
	httpReq.Header.Set("Cookie", "access_token="+accessToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send create-chat request: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read create-chat response: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("arcee create-chat failed: status=%d body=%s", resp.StatusCode, string(rawBody))
	}

	return parseCreateChatResponse(string(rawBody))
}

// CreateChatStream 真正的流式调用：逐块读取 Arcee 响应体，
// 解析出 content 部分后通过 onChunk(text) 实时回调，最终返回完整 result。
// 401 错误在开始流式传输前检测，可在上层安全重试。
func (c *Client) CreateChatStream(ctx context.Context, accessToken string, reqBody CreateChatRequest, onChunk func(string)) (*CreateChatResult, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required")
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal create-chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/completions/create-chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create create-chat request: %w", err)
	}

	if err := setBrowserHeaders(httpReq); err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/plain")
	httpReq.Header.Set("Cookie", "access_token="+accessToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send create-chat request: %w", err)
	}
	defer resp.Body.Close()

	// 在任何流式写出前先检查状态码，401 可安全重试
	if resp.StatusCode >= http.StatusBadRequest {
		rawBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("arcee create-chat failed: status=%d body=%s", resp.StatusCode, string(rawBody))
	}

	// 状态机：
	//   0 = 等待 __STREAM_INIT_END__（跳过 init 段）
	//   1 = 正在接收 content（实时回调）
	//   2 = 已遇到 __METADATA__（停止回调，收集 metadata）
	var (
		buf        strings.Builder
		contentBuf strings.Builder
		phase      = 0
		scanner    = bufio.NewScanner(resp.Body)
	)
	// 允许单行最大 1 MB，防止超长行截断
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')

		switch phase {
		case 0:
			if strings.Contains(buf.String(), streamInitEnd) {
				phase = 1
			}
		case 1:
			if strings.Contains(line, metadataStart) {
				phase = 2
				break
			}
			chunk := line + "\n"
			contentBuf.WriteString(chunk)
			if onChunk != nil {
				onChunk(chunk)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read streaming body: %w", err)
	}

	result, err := parseCreateChatResponse(buf.String())
	if err != nil {
		// metadata 解析失败时仍返回已收集的内容
		return &CreateChatResult{Content: strings.TrimSpace(contentBuf.String()), Raw: buf.String()}, nil
	}
	return result, nil
}

func parseCreateChatResponse(raw string) (*CreateChatResult, error) {
	result := &CreateChatResult{Raw: raw}

	initStart := strings.Index(raw, streamInitStart)
	initEnd := strings.Index(raw, streamInitEnd)
	metaStart := strings.Index(raw, metadataStart)
	metaEnd := strings.Index(raw, metadataEnd)

	if initStart == -1 || initEnd == -1 || metaStart == -1 || metaEnd == -1 {
		return nil, fmt.Errorf("unexpected create-chat response format")
	}

	initJSON := raw[initStart+len(streamInitStart) : initEnd]
	if err := json.Unmarshal([]byte(initJSON), &result.Init); err != nil {
		return nil, fmt.Errorf("decode stream init: %w", err)
	}

	content := raw[initEnd+len(streamInitEnd) : metaStart]
	result.Content = strings.TrimSpace(content)

	metaJSON := raw[metaStart+len(metadataStart) : metaEnd]
	if err := json.Unmarshal([]byte(metaJSON), &result.Metadata); err != nil {
		return nil, fmt.Errorf("decode stream metadata: %w", err)
	}

	return result, nil
}
