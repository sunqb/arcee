package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"arcee/arcee"
	appconfig "arcee/config"
)

type chatCompletionsRequest struct {
	Model       string            `json:"model"`
	Messages    []chatMessage     `json:"messages"`
	Temperature *float64          `json:"temperature,omitempty"`
	Stream      bool              `json:"stream,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	ToolChoice  any               `json:"tool_choice,omitempty"`
}

type chatMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionsResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
}

type chatCompletionChoice struct {
	Index        int              `json:"index"`
	Message      assistantMessage `json:"message,omitempty"`
	Delta        assistantMessage `json:"delta,omitempty"`
	FinishReason string           `json:"finish_reason,omitempty"`
}

type assistantMessage struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

// tokenEntry 持有单个账号的凭证，支持线程安全的 token 刷新。
type tokenEntry struct {
	mu       sync.Mutex
	token    string
	email    string
	password string
	filePath string // 空表示无文件（env var 模式），刷新后不写回
}

func (e *tokenEntry) currentToken() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.token
}

// refreshToken 用 email+password 重新登录，更新内存 token 并写回文件。
func (e *tokenEntry) refreshToken(ctx context.Context, client *arcee.Client) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.email == "" || e.password == "" {
		return fmt.Errorf("no credentials available for token refresh")
	}

	resp, err := client.Login(ctx, arcee.LoginRequest{
		Email:      e.email,
		Password:   e.password,
		RememberMe: false,
	})
	if err != nil {
		return fmt.Errorf("re-login: %w", err)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil || payload.AccessToken == "" {
		return fmt.Errorf("re-login response missing access_token")
	}

	e.token = payload.AccessToken
	if e.filePath != "" {
		_ = appconfig.SaveAccessTokenFile(e.filePath, e.token, e.email, e.password, "")
	}
	log.Printf("token refreshed for %s", e.email)
	return nil
}

// createChat 调用 CreateChat，遇 401 时自动刷新 token 并重试一次。
func (e *tokenEntry) createChat(ctx context.Context, client *arcee.Client, req arcee.CreateChatRequest) (*arcee.CreateChatResult, error) {
	result, err := client.CreateChat(ctx, e.currentToken(), req)
	if err != nil && isUnauthorized(err) {
		if refreshErr := e.refreshToken(ctx, client); refreshErr != nil {
			log.Printf("token refresh failed: %v", refreshErr)
			return nil, err
		}
		result, err = client.CreateChat(ctx, e.currentToken(), req)
	}
	return result, err
}

// createChatStream 流式调用，遇 401 时自动刷新 token 并重试一次。
// 401 在第一个 chunk 发出前检测，重试安全。
func (e *tokenEntry) createChatStream(ctx context.Context, client *arcee.Client, req arcee.CreateChatRequest, onChunk func(string)) (*arcee.CreateChatResult, error) {
	result, err := client.CreateChatStream(ctx, e.currentToken(), req, onChunk)
	if err != nil && isUnauthorized(err) {
		if refreshErr := e.refreshToken(ctx, client); refreshErr != nil {
			log.Printf("token refresh failed: %v", refreshErr)
			return nil, err
		}
		result, err = client.CreateChatStream(ctx, e.currentToken(), req, onChunk)
	}
	return result, err
}

func isUnauthorized(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status=401")
}

// tokenPool 线程安全的 RoundRobin token 池
type tokenPool struct {
	entries []*tokenEntry
	counter atomic.Uint64
}

func newTokenPool(entries []*tokenEntry) *tokenPool {
	return &tokenPool{entries: entries}
}

func (p *tokenPool) nextEntry() *tokenEntry {
	if len(p.entries) == 0 {
		return nil
	}
	idx := p.counter.Add(1) - 1
	return p.entries[idx%uint64(len(p.entries))]
}

func runServer(cfg *appconfig.Config) {
	// 优先从 tokens/ 目录加载多个 token（含 email/password 凭证，支持自动刷新）
	tokenFiles, err := appconfig.LoadAllTokenFilesFromDir(appconfig.DefaultTokensDir)
	if err != nil {
		log.Fatal(err)
	}

	var entries []*tokenEntry
	for _, tf := range tokenFiles {
		entries = append(entries, &tokenEntry{
			token:    tf.File.AccessToken,
			email:    tf.File.Email,
			password: tf.File.Password,
			filePath: tf.Path,
		})
	}

	// fallback：tokens 目录为空时，读取单个 access_token（兼容旧方式，无法自动刷新）
	if len(entries) == 0 {
		single, err := cfg.Server.ResolvedAccessToken()
		if err != nil {
			log.Fatal("no tokens found: either populate tokens/ dir or set access_token in config")
		}
		entries = []*tokenEntry{{token: single}}
	}

	log.Printf("loaded %d token(s)", len(entries))
	pool := newTokenPool(entries)

	httpClient := &http.Client{Timeout: 60 * time.Second}
	arceeClient := arcee.NewClient(arcee.WithHTTPClient(httpClient))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	modelsHandler := func(w http.ResponseWriter, r *http.Request) {
		if !authorize(cfg.Server, w, r) {
			return
		}
		models := make([]map[string]any, 0, len(cfg.Server.SupportedModels()))
		for _, model := range cfg.Server.SupportedModels() {
			models = append(models, map[string]any{
				"id":       model,
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "arcee",
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   models,
		})
	}
	mux.HandleFunc("/v1/models", modelsHandler)
	mux.HandleFunc("/models", modelsHandler)
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authorize(cfg.Server, w, r) {
			return
		}
		handleChatCompletions(cfg.Server, pool.nextEntry(), arceeClient, w, r)
	})

	server := &http.Server{
		Addr:              cfg.Server.ResolvedListen(),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("openai-compatible gateway listening on http://%s", cfg.Server.ResolvedListen())
	log.Fatal(server.ListenAndServe())
}

func handleChatCompletions(cfg appconfig.ServerConfig, entry *tokenEntry, client *arcee.Client, w http.ResponseWriter, r *http.Request) {
	var req chatCompletionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	prompt := buildConversationPrompt(req.Messages)
	if prompt == "" {
		http.Error(w, "at least one message with content is required", http.StatusBadRequest)
		return
	}

	temp := 0.3
	if req.Temperature != nil {
		temp = *req.Temperature
	}
	modelName := resolveModelName(cfg, req.Model)
	enabledTools := resolveEnabledTools(cfg, req.Tools)

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	if len(req.Tools) > 0 && !hasToolMessages(req.Messages) {
		planResult, err := entry.createChat(ctx, client, arcee.CreateChatRequest{
			Message:            buildToolPlannerPrompt(req.Messages, req.Tools, req.ToolChoice),
			Title:              buildTitle(req.Messages),
			BaseModelName:      modelName,
			EnabledTools:       enabledTools,
			FileReferences:     []any{},
			Temperature:        temp,
			ProviderPreference: nil,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		planned, err := parsePlannerResponse(planResult.Content)
		if err == nil && planned.Type == "tool_calls" && len(planned.ToolCalls) > 0 {
			toolCalls := toOpenAIToolCalls(planned.ToolCalls)
			if allSupportedLocalTools(toolCalls) {
				toolMessages, execErr := executeLocalToolCalls(toolCalls)
				if execErr == nil {
					followupMessages := appendToolMessages(req.Messages, toolCalls, toolMessages)
					finalPrompt := buildConversationPrompt(followupMessages)
					finalReq := arcee.CreateChatRequest{
						Message:            finalPrompt,
						Title:              buildTitle(req.Messages),
						BaseModelName:      modelName,
						EnabledTools:       enabledTools,
						FileReferences:     []any{},
						Temperature:        temp,
						ProviderPreference: nil,
					}
					if req.Stream {
						streamChatToClient(ctx, w, entry, client, finalReq, modelName)
						return
					}
					finalResult, finalErr := entry.createChat(ctx, client, finalReq)
					if finalErr == nil {
						writeJSON(w, http.StatusOK, chatCompletionsResponse{
							ID:      "chatcmpl-" + shortID(),
							Object:  "chat.completion",
							Created: time.Now().Unix(),
							Model:   modelName,
							Choices: []chatCompletionChoice{{
								Index: 0,
								Message: assistantMessage{
									Role:    "assistant",
									Content: finalResult.Content,
								},
								FinishReason: "stop",
							}},
						})
						return
					}
				}
			}
			if req.Stream {
				writeToolCallStreamResponse(w, modelName, toolCalls)
				return
			}
			writeJSON(w, http.StatusOK, chatCompletionsResponse{
				ID:      "chatcmpl-" + shortID(),
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []chatCompletionChoice{{
					Index: 0,
					Message: assistantMessage{
						Role:      "assistant",
						ToolCalls: toolCalls,
					},
					FinishReason: "tool_calls",
				}},
			})
			return
		}

		if err == nil && planned.Type == "final" {
			if req.Stream {
				writeStreamResponse(w, modelName, &arcee.CreateChatResult{Content: planned.Content})
				return
			}
			writeJSON(w, http.StatusOK, chatCompletionsResponse{
				ID:      "chatcmpl-" + shortID(),
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []chatCompletionChoice{{
					Index: 0,
					Message: assistantMessage{
						Role:    "assistant",
						Content: planned.Content,
					},
					FinishReason: "stop",
				}},
			})
			return
		}
	}

	mainReq := arcee.CreateChatRequest{
		Message:            prompt,
		Title:              buildTitle(req.Messages),
		BaseModelName:      modelName,
		EnabledTools:       enabledTools,
		FileReferences:     []any{},
		Temperature:        temp,
		ProviderPreference: nil,
	}
	if req.Stream {
		streamChatToClient(ctx, w, entry, client, mainReq, modelName)
		return
	}
	result, err := entry.createChat(ctx, client, mainReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, chatCompletionsResponse{
		ID:      "chatcmpl-" + shortID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []chatCompletionChoice{{
			Index: 0,
			Message: assistantMessage{
				Role:    "assistant",
				Content: result.Content,
			},
			FinishReason: "stop",
		}},
	})
}

func resolveModelName(cfg appconfig.ServerConfig, requested string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return cfg.ResolvedModel()
	}
	for _, model := range cfg.SupportedModels() {
		if model == requested {
			return requested
		}
	}
	return cfg.ResolvedModel()
}

func resolveEnabledTools(cfg appconfig.ServerConfig, requestTools []json.RawMessage) []string {
	if len(cfg.EnabledTools) > 0 {
		return cfg.EnabledTools
	}

	enabled := []string{}
	for _, raw := range requestTools {
		if strings.Contains(strings.ToLower(string(raw)), "web_search") {
			enabled = append(enabled, "web_search")
			break
		}
	}
	return enabled
}

func authorize(cfg appconfig.ServerConfig, w http.ResponseWriter, r *http.Request) bool {
	if cfg.OpenAIAPIKey == "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) != "Bearer "+cfg.OpenAIAPIKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func buildPrompt(messages []chatMessage) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		content := stringifyContent(message.Content)
		if content == "" {
			continue
		}
		role := message.Role
		if role == "" {
			role = "user"
		}
		parts = append(parts, strings.ToUpper(role)+": "+content)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func buildTitle(messages []chatMessage) string {
	for _, message := range messages {
		if message.Role == "user" {
			if content := stringifyContent(message.Content); content != "" {
				return firstNRunes(content, 80)
			}
		}
	}
	return "New Chat"
}

func stringifyContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func firstNRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// streamChatToClient 真正的流式转发：调用 CreateChatStream，
// 每个 onChunk 回调立即写一个 SSE delta event 并 flush。
func streamChatToClient(ctx context.Context, w http.ResponseWriter, entry *tokenEntry, client *arcee.Client, req arcee.CreateChatRequest, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	id := "chatcmpl-" + shortID()
	created := time.Now().Unix()
	sentRole := false

	_, err := entry.createChatStream(ctx, client, req, func(chunk string) {
		if !sentRole {
			writeSSE(w, chatCompletionsResponse{
				ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []chatCompletionChoice{{Index: 0, Delta: assistantMessage{Role: "assistant"}}},
			})
			sentRole = true
		}
		writeSSE(w, chatCompletionsResponse{
			ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []chatCompletionChoice{{Index: 0, Delta: assistantMessage{Content: chunk}}},
		})
	})
	if err != nil {
		writeSSE(w, map[string]any{"error": err.Error()})
		flusher.Flush()
		return
	}

	writeSSE(w, chatCompletionsResponse{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []chatCompletionChoice{{Index: 0, Delta: assistantMessage{}, FinishReason: "stop"}},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeStreamResponse(w http.ResponseWriter, model string, result *arcee.CreateChatResult) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	writeSSE(w, chatCompletionsResponse{
		ID:      "chatcmpl-" + shortID(),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []chatCompletionChoice{{
			Index: 0,
			Delta: assistantMessage{
				Role:    "assistant",
				Content: result.Content,
			},
		}},
	})

	writeSSE(w, chatCompletionsResponse{
		ID:      "chatcmpl-" + shortID(),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []chatCompletionChoice{{
			Index:        0,
			Delta:        assistantMessage{},
			FinishReason: "stop",
		}},
	})

	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeToolCallStreamResponse(w http.ResponseWriter, model string, toolCalls []openAIToolCall) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	writeSSE(w, chatCompletionsResponse{
		ID:      "chatcmpl-" + shortID(),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []chatCompletionChoice{{
			Index: 0,
			Delta: assistantMessage{
				Role:      "assistant",
				ToolCalls: toolCalls,
			},
		}},
	})

	writeSSE(w, chatCompletionsResponse{
		ID:      "chatcmpl-" + shortID(),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []chatCompletionChoice{{
			Index:        0,
			Delta:        assistantMessage{},
			FinishReason: "tool_calls",
		}},
	})

	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeSSE(w http.ResponseWriter, payload any) {
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func shortID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
