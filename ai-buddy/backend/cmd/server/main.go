package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"aibuddy/internal/app"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	port := env("PORT", "8080")
	st, err := app.Open(env("DATABASE_URL", "postgres://aibuddy:aibuddy@localhost:5432/aibuddy?sslmode=disable"))
	if err != nil {
		log.Fatal(err)
	}
	if err := st.Init(context.Background()); err != nil {
		log.Fatal(err)
	}
	abs := &app.HTTPAbsence{BaseURL: env("ABSENCE_URL", "http://localhost:"+port+"/mock/absence"), HTTP: &http.Client{Timeout: 5 * time.Second}}
	llm := &app.LLM{BaseURL: env("LLM_BASE_URL", "https://api.openai.com/v1"), Key: os.Getenv("LLM_API_KEY"), Model: env("LLM_MODEL", "gpt-4o-mini"), HTTP: &http.Client{}}
	log.Printf("AI-buddy backend on :%s (LLM enabled: %v)", port, llm.Enabled())
	log.Fatal(http.ListenAndServe(":"+port, app.New(st, abs, llm).Handler()))
}
