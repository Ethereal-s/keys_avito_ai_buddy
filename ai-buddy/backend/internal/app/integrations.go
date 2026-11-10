package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ---- Имитация корпоративной системы отсутствий ----

type AbsenceResult struct {
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Approver   string `json:"approver"`
}

type AbsenceClient interface {
	Create(ctx context.Context, login, from, to string) (AbsenceResult, error)
}

type HTTPAbsence struct {
	BaseURL string
	HTTP    *http.Client
}

func (a *HTTPAbsence) Create(ctx context.Context, login, from, to string) (AbsenceResult, error) {
	b, _ := json.Marshal(map[string]string{"employee": login, "from": from, "to": to, "type": "day_off"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/requests", bytes.NewReader(b))
	if err != nil {
		return AbsenceResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return AbsenceResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return AbsenceResult{}, fmt.Errorf("absence service status %d", resp.StatusCode)
	}
	var out AbsenceResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return AbsenceResult{}, err
	}
	return out, nil
}

// ---- LLM (OpenAI-совместимый API) ----

type LLM struct {
	BaseURL, Key, Model string
	HTTP                *http.Client
}

func (l *LLM) Enabled() bool { return l != nil && l.Key != "" }

const systemPrompt = "Ты AI-buddy — помощник стажёра. Отвечай кратко по-русски, строго на основе предоставленной инструкции. " +
	"Если ответа в инструкции нет — скажи об этом и посоветуй обратиться к наставнику. Не выдумывай правил."

func (l *LLM) Answer(ctx context.Context, q string, d Doc) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": l.Model, "temperature": 0.2,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": fmt.Sprintf("Инструкция «%s»:\n%s\n\nВопрос стажёра: %s", d.Title, d.Body, q)},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.Key)
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("llm status %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("llm: empty answer")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
