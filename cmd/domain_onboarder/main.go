package main

import (
	"log"
	"net/http"
	"os"

	"github.com/example/logistics-custom-domain-onboarding/internal"
)

func main() {
	apiKey := os.Getenv("INFRAI_API_KEY")
	if apiKey == "" {
		log.Fatal("INFRAI_API_KEY is required")
	}
	webhookSecret := os.Getenv("WEBHOOK_SECRET")
	if webhookSecret == "" {
		log.Fatal("WEBHOOK_SECRET is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	infrai := internal.NewInfraiClient(apiKey)
	service := internal.NewTenantOnboardingService(infrai, webhookSecret)

	mux := http.NewServeMux()
	mux.HandleFunc("/tenants/onboard", service.HandleOnboardTenant)
	mux.HandleFunc("/webhooks/domain-verification", service.HandleVerificationWebhook)

	log.Printf("listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
