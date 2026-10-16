package internal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestBuildTrackingURLSwitchesHostAfterVerification(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		expected  string
	}{
		{
			name:     "pending uses platform host",
			status:   "pending_verification",
			expected: "https://tracking.example.internal/shipments/ZX-42",
		},
		{
			name:     "verified uses tenant host",
			status:   "verified",
			expected: "https://track.acme.example/shipments/ZX-42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &TenantDomainState{
				TrackingDomain:   "track.acme.example",
				OnboardingStatus: tt.status,
				Shipment: Shipment{
					TrackingNumber: "ZX-42",
				},
			}
			got := BuildTrackingURL(state)
			if got != tt.expected {
				t.Fatalf("BuildTrackingURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"domain":"track.acme.example"}`)
	timestamp := "1735689600"
	secret := "dev-secret"

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	if !VerifyWebhookSignature(body, timestamp, signature, secret) {
		t.Fatal("expected signature to verify")
	}
}
