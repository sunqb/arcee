package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"arcee/arcee"
	appconfig "arcee/config"
	"arcee/yydsmail"
)

func runSignup(cfg *appconfig.Config, count int) {
	if cfg.Signup.APIKey == "" {
		log.Fatal("config.signup.api_key is required")
	}
	if count < 1 {
		count = 1
	}

	// 确保 tokens 目录存在
	if err := os.MkdirAll(appconfig.DefaultTokensDir, 0o755); err != nil {
		log.Fatalf("create tokens dir: %v", err)
	}

	httpClient := &http.Client{Timeout: 20 * time.Second}
	mailClient := yydsmail.NewClient(
		yydsmail.WithAPIKey(cfg.Signup.APIKey),
		yydsmail.WithHTTPClient(httpClient),
	)
	arceeClient := arcee.NewClient(arcee.WithHTTPClient(httpClient))
	ctx := context.Background()

	successCount := 0
	for i := 0; i < count; i++ {
		log.Printf("[signup %d/%d] starting...", i+1, count)

		signupResp, err := arcee.ProvisionAndSignupFlow(
			ctx,
			arceeClient,
			mailClient,
			"",
			cfg.Signup.Domain,
			"random",
		)
		if err != nil {
			log.Printf("[signup %d/%d] failed at signup: %v", i+1, count, err)
			continue
		}

		identity := signupResp.Identity
		fmt.Printf("[signup %d/%d] email=%s\n", i+1, count, identity.Email)

		_, link, err := arcee.WaitForVerifyLink(
			ctx,
			mailClient,
			identity.Email,
			10,
			2*time.Second,
			20*time.Second,
		)
		if err != nil {
			log.Printf("[signup %d/%d] failed waiting verify link: %v", i+1, count, err)
			continue
		}

		status, err := arcee.ConfirmLink(ctx, httpClient, link)
		if err != nil {
			log.Printf("[signup %d/%d] failed confirming link: %v", i+1, count, err)
			continue
		}
		fmt.Printf("[signup %d/%d] verified status=%d\n", i+1, count, status)

		loginResp, err := arcee.LoginAfterVerification(ctx, arceeClient, identity)
		if err != nil {
			log.Printf("[signup %d/%d] failed login: %v", i+1, count, err)
			continue
		}

		accessToken := extractAccessToken(loginResp)
		if accessToken == "" {
			log.Printf("[signup %d/%d] empty access_token, skipping", i+1, count)
			continue
		}

		// 写入 tokens/ 目录，文件名用时间戳保证唯一
		tokenPath := fmt.Sprintf("%s/token_%d.json", appconfig.DefaultTokensDir, time.Now().UnixNano())
		if err := appconfig.SaveAccessTokenFile(tokenPath, accessToken, identity.Email, identity.Password, link); err != nil {
			log.Printf("[signup %d/%d] failed saving token: %v", i+1, count, err)
			continue
		}

		fmt.Printf("[signup %d/%d] access_token saved to %s\n", i+1, count, tokenPath)
		successCount++

		// 多账号注册时两次注册之间稍作间隔，避免被限流
		if i < count-1 {
			time.Sleep(1 * time.Second)
		}
	}

	fmt.Printf("signup done: %d/%d succeeded\n", successCount, count)
	if successCount == 0 {
		log.Fatal("no accounts registered successfully")
	}
}

func extractAccessToken(loginResp *arcee.LoginResult) string {
	if loginResp == nil || len(loginResp.Response.Body) == 0 {
		return ""
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(loginResp.Response.Body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.AccessToken)
}
