package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var globalModelNames = map[string]string{
	"auto":                "Auto",
	"qmodel_preview":      "Qwen3.8-Max-Preview",
	"qwen3.8-max-preview": "Qwen3.8-Max-Preview",
	"qmodel_latest":       "Qwen3.7-Max",
	"qwen3.7-max":         "Qwen3.7-Max",
	"qmodel":              "Qwen3.7-Plus",
	"qwen3.7-plus":        "Qwen3.7-Plus",
}

func globalModelName(model string) string {
	model = strings.ToLower(stripProviderPrefix(strings.TrimSpace(model)))
	if mapped := globalModelNames[model]; mapped != "" {
		return mapped
	}
	return stripProviderPrefix(model)
}

func qoderCLIOutput(model string, payload []byte) ([]string, error) {
	var req openAIRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("global qoder payload: %w", err)
	}
	prompt := extractLatestUserPrompt(req.Messages)
	if prompt == "" {
		return nil, fmt.Errorf("global qoder: no user message")
	}
	cmd := exec.Command(
		configuredCLIPath(),
		"-p",
		"--output-format", "stream-json",
		"--no-session-persistence",
		"--tools", "",
		"-m", globalModelName(model),
		prompt,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("global qodercli: %s", redactSecrets(msg))
	}
	var texts []string
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "assistant" {
			continue
		}
		for _, content := range event.Message.Content {
			if content.Type == "text" && content.Text != "" {
				texts = append(texts, content.Text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("global qodercli output: %w", err)
	}
	if len(texts) == 0 {
		return nil, fmt.Errorf("global qodercli returned no assistant text")
	}
	return texts, nil
}

func globalCompletion(model string, texts []string) []byte {
	content := strings.Join(texts, "")
	out, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-qoder-global", "object": "chat.completion",
		"created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	})
	return out
}

func handleGlobalExecute(req pluginapi.ExecutorRequest) ([]byte, error) {
	texts, err := qoderCLIOutput(req.Model, req.Payload)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: globalCompletion(req.Model, texts)})
}

func handleGlobalStream(req executorStreamRequest) ([]byte, error) {
	payload := req.Payload
	if len(payload) == 0 {
		payload = req.OriginalRequest
	}
	texts, err := qoderCLIOutput(req.Model, payload)
	if err != nil {
		return nil, err
	}
	chunks := make([]pluginapi.ExecutorStreamChunk, 0, len(texts)+1)
	for _, text := range texts {
		raw, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-qoder-global", "object": "chat.completion.chunk",
			"created": time.Now().Unix(), "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}},
		})
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: raw})
	}
	done, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-qoder-global", "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": req.Model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	})
	chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: done})
	return okEnvelope(streamResponse{Headers: streamHeaders(), Chunks: chunks})
}

func verifyGlobalCLI() error {
	cmd := exec.Command(configuredCLIPath(), "--list-models")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("global qodercli is unavailable or logged out: %s", redactSecrets(strings.TrimSpace(string(output))))
	}
	if !strings.Contains(string(output), "Qwen3.8-Max-Preview") {
		return fmt.Errorf("global qodercli model catalog does not include Qwen3.8-Max-Preview")
	}
	return nil
}

func handleGlobalStartLogin() ([]byte, error) {
	if err := verifyGlobalCLI(); err != nil {
		return nil, err
	}
	now := time.Now()
	state := fmt.Sprintf("qw-global-%d", now.UnixNano())
	loginStates.Store(state, &loginCtx{expires: now.Add(loginTTL), startedAt: now.UnixNano()})
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       "https://qoder.com/account",
		State:     state,
		ExpiresAt: now.Add(loginTTL).UTC(),
		Metadata: map[string]any{
			"prompt": "Using the authenticated local qodercli session. No China credential is created or reused.",
		},
	})
}

func handleGlobalPollLogin(state string) ([]byte, error) {
	if err := verifyGlobalCLI(); err != nil {
		return nil, err
	}
	loginStates.Delete(state)
	sa := &storedAuth{
		Auth: storedTokens{
			AccessToken: "qodercli-session",
			ExpiresAt:   time.Now().AddDate(10, 0, 0).Unix(),
			Domain:      "qoder.com",
		},
		Account: storedAccount{UID: "global-cli", Nickname: "Qoder Global CLI"},
	}
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auth:   toAuthData(sa),
	})
}
