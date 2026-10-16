package internal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ShipmentEvent struct {
	Code     string `json:"code"`
	At       string `json:"at"`
	Location string `json:"location"`
	Note     string `json:"note"`
}

type ProofOfDeliveryFile struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type DeliveryException struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Shipment struct {
	ShipmentID           string                 `json:"shipment_id"`
	TrackingNumber       string                 `json:"tracking_number"`
	Events               []ShipmentEvent        `json:"events"`
	ProofOfDeliveryFiles []ProofOfDeliveryFile  `json:"proof_of_delivery_files"`
	Exceptions           []DeliveryException    `json:"exceptions"`
}

type OnboardTenantRequest struct {
	TenantID       string   `json:"tenant_id"`
	AccountID      string   `json:"account_id"`
	CompanyName    string   `json:"company_name"`
	TrackingDomain string   `json:"tracking_domain"`
	WebhookURL     string   `json:"webhook_url"`
	Shipment       Shipment `json:"shipment"`
}

type TenantDomainState struct {
	TenantID          string    `json:"tenant_id"`
	AccountID         string    `json:"account_id"`
	CompanyName       string    `json:"company_name"`
	TrackingDomain    string    `json:"tracking_domain"`
	ZoneID            string    `json:"zone_id"`
	OnboardingStatus  string    `json:"onboarding_status"`
	WebhookID         string    `json:"webhook_id"`
	VerificationAt    time.Time `json:"verification_at,omitempty"`
	Shipment          Shipment  `json:"shipment"`
	TrackingURL       string    `json:"tracking_url"`
}

type verificationWebhookPayload struct {
	Domain string `json:"domain"`
}

type domainAPI interface {
	DNSDomainAdd(ctx context.Context, req DomainAddRequest) (*DomainGetResponse, error)
	DNSDomainGet(ctx context.Context, domain string) (*DomainGetResponse, error)
	DNSRecordUpsert(ctx context.Context, req RecordUpsertRequest) error
	DNSDomainVerify(ctx context.Context, req DomainVerifyRequest) error
	AccountWebhooksRegister(ctx context.Context, req WebhookRegisterRequest) (*WebhookRegisterResponse, error)
}

type TenantOnboardingService struct {
	infrai        domainAPI
	webhookSecret string
	mu            sync.Mutex
	state         map[string]*TenantDomainState
}

func NewTenantOnboardingService(infrai domainAPI, webhookSecret string) *TenantOnboardingService {
	return &TenantOnboardingService{
		infrai:        infrai,
		webhookSecret: webhookSecret,
		state:         make(map[string]*TenantDomainState),
	}
}

func (s *TenantOnboardingService) HandleOnboardTenant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req OnboardTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state, err := s.OnboardTenant(r.Context(), req)
	if err != nil {
		status := http.StatusBadGateway
		var infraErr *InfraiError
		if ok := AsInfraiError(err, &infraErr); ok && infraErr.StatusCode >= 400 && infraErr.StatusCode < 500 {
			status = infraErr.StatusCode
		}
		http.Error(w, err.Error(), status)
		return
	}

	writeJSON(w, http.StatusAccepted, state)
}

func (s *TenantOnboardingService) HandleVerificationWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !VerifyWebhookSignature(body, r.Header.Get("X-Infrai-Timestamp"), r.Header.Get("X-Infrai-Signature"), s.webhookSecret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var payload verificationWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.MarkDomainVerified(payload.Domain)
	writeJSON(w, http.StatusOK, map[string]string{"status": "verified"})
}

func (s *TenantOnboardingService) OnboardTenant(ctx context.Context, req OnboardTenantRequest) (*TenantDomainState, error) {
	domainInfo, err := s.infrai.DNSDomainAdd(ctx, DomainAddRequest{
		Domain:    req.TrackingDomain,
		Vendor:    "logistics-platform",
		AccountID: req.AccountID,
		Metadata: map[string]interface{}{
			"tenant_id": req.TenantID,
		},
	})
	if err != nil {
		domainInfo, err = s.infrai.DNSDomainGet(ctx, req.TrackingDomain)
		if err != nil {
			return nil, err
		}
	}

	zoneID := domainInfo.ZoneID

	if err := s.infrai.DNSRecordUpsert(ctx, RecordUpsertRequest{
		ZoneID:     zoneID,
		RecordType: "CNAME",
		Name:       "track",
		Content:    "tracking.example.internal",
		TTL:        300,
		Metadata: map[string]interface{}{
			"tenant_id": req.TenantID,
		},
	}); err != nil {
		return nil, err
	}

	if err := s.infrai.DNSRecordUpsert(ctx, RecordUpsertRequest{
		ZoneID:     zoneID,
		RecordType: "TXT",
		Name:       "_tracking-verification",
		Content:    req.TenantID,
		TTL:        300,
		Metadata: map[string]interface{}{
			"tenant_id": req.TenantID,
		},
	}); err != nil {
		return nil, err
	}

	wh, err := s.infrai.AccountWebhooksRegister(ctx, WebhookRegisterRequest{
		URL:         req.WebhookURL,
		Events:      []string{"dns.domain.verified"},
		Description: fmt.Sprintf("domain verification for %s", req.TenantID),
		Secret:      s.webhookSecret,
		Headers: map[string]string{
			"X-Tenant-ID": req.TenantID,
		},
	})
	if err != nil {
		return nil, err
	}

	if err := s.infrai.DNSDomainVerify(ctx, DomainVerifyRequest{Domain: req.TrackingDomain}); err != nil {
		return nil, err
	}

	state := &TenantDomainState{
		TenantID:         req.TenantID,
		AccountID:        req.AccountID,
		CompanyName:      req.CompanyName,
		TrackingDomain:   req.TrackingDomain,
		ZoneID:           zoneID,
		OnboardingStatus: "pending_verification",
		WebhookID:        wh.ID,
		Shipment:         req.Shipment,
	}
	state.TrackingURL = BuildTrackingURL(state)

	s.mu.Lock()
	s.state[req.TenantID] = state
	s.mu.Unlock()

	return state, nil
}

func (s *TenantOnboardingService) MarkDomainVerified(domain string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tenant := range s.state {
		if strings.EqualFold(tenant.TrackingDomain, domain) {
			tenant.OnboardingStatus = "verified"
			tenant.VerificationAt = time.Now().UTC()
			tenant.TrackingURL = BuildTrackingURL(tenant)
		}
	}
}

func BuildTrackingURL(state *TenantDomainState) string {
	host := "tracking.example.internal"
	if state.OnboardingStatus == "verified" {
		host = state.TrackingDomain
	}
	return fmt.Sprintf("https://%s/shipments/%s", host, state.Shipment.TrackingNumber)
}

func VerifyWebhookSignature(body []byte, timestamp, signature, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func AsInfraiError(err error, target **InfraiError) bool {
	if err == nil {
		return false
	}
	ie, ok := err.(*InfraiError)
	if !ok {
		return false
	}
	*target = ie
	return true
}
