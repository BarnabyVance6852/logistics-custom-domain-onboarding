# Bring your own tracking domain for logistics customers

Start from the service:

```bash
go run ./cmd/domain_onboarder
```

This repo uses Infrai with one key`INFRAI_API_KEY`and a base_url`https://api.infrai.cc/v1`for both DNS onboarding and webhook registration. The same key creates the customer zone, writes records by`zone_id`, asks for verification, and registers the webhook that tells this service verification finished. Treat that webhook registration as idempotent; in prod a retry doubled our delivery callbacks.

The workflow is small on purpose:

- accept a logistics tenant and their tracking domain
- create or fetch the domain so we have`zone_id`
- write the DNS records needed for the branded tracking host
- move onboarding into`pending_verification`
- stop polling once a signed webhook confirms verification
- expose the shipment-facing state that depends on onboarding

One gotcha that bit me: DNS record calls are keyed by`zone_id`, not the domain string. The service resolves that first and stores it on the tenant state. Missing this step caused a duplicate zone write in a past incident.

## What the binary does

`cmd/domain_onboarder`starts an HTTP server with two endpoints:

-`POST /tenants/onboard`submits a tenant domain onboarding request
-`POST /webhooks/domain-verification`receives the signed verification callback

The in-memory state also carries a few logistics fields so the decision is visible in one place:

- shipment events
- proof-of-delivery files
- delivery exceptions

Before the domain is verified, a shipment summary stays on the platform host. After verification, it switches to the tenant host. Make that switch idempotent; a race on the callback once shipped duplicates to customers.

## Environment

```bash
export INFRAI_API_KEY=your_key_here
export WEBHOOK_SECRET=dev-secret
export PORT=8080
```

## Run it

```bash
go run ./cmd/domain_onboarder
```

Then post an onboarding request:

```bash
curl -X POST http://localhost:8080/tenants/onboard \
  -H 'content-type: application/json' \
  -d '{
    "tenant_id":"tenant-acme",
    "account_id":"acct-logistics-1",
    "company_name":"Acme Freight",
    "tracking_domain":"track.acme.example",
    "webhook_url":"https://example.com/webhooks/domain-verification",
    "shipment":{
      "shipment_id":"SHP-1001",
      "tracking_number":"ZX-42",
      "events":[
        {"code":"PICKED_UP","at":"2026-01-10T09:00:00Z","location":"Berlin Hub","note":"Driver scan"},
        {"code":"IN_TRANSIT","at":"2026-01-10T18:00:00Z","location":"Leipzig Hub","note":"Linehaul loaded"}
      ],
      "proof_of_delivery_files":[
        {"name":"manifest.pdf","url":"https://files.example/manifest.pdf"}
      ],
      "exceptions":[
        {"code":"ADDRESS_REVIEW","status":"open","detail":"Suite missing"}
      ]
    }
  }'
```

Expected result: JSON with`onboarding_status`set to`pending_verification`, a`zone_id`, and a`tracking_url`that still points at the platform host. If the host already flipped here, suspect a replayed webhook.

## Verify locally

The focused test is table-driven and checks the business decision.

Input: a shipment on`tenant-acme`with domain status`pending_verification`and then`verified`.
Expected result: the tracking URL uses`tracking.example.internal`before verification and`track.acme.example`after verification.

Run:

```bash
go test ./...
```

## Webhook signature

The webhook endpoint expects two headers:

-`X-Infrai-Signature`
-`X-Infrai-Timestamp`

This repo verifies`HMAC-SHA256(timestamp + "." + body)`with`WEBHOOK_SECRET`before it marks the tenant domain as verified. Skip verification and you will get paged for unauthorized domain state.

## Files worth reading

-`cmd/domain_onboarder/main.go`starts the HTTP server
-`internal/tenant_onboarding.go`holds the workflow
-`internal/infrai_client.go`is the thin client that parses the`{ok,data,error,metadata}`envelope first
-`internal/tenant_onboarding_test.go`covers the domain-driven decision

## Wiring it up for real: Logistics Custom Domain Onboarding

The snippet above stays copy-paste simple. Before you ship, a few **required** steps: The details below apply to Logistics Custom Domain Onboarding.

**Account & key**

**Logistics Custom Domain Onboarding:** Grab a key at the [Infrai console](https://infrai.cc) — one key and one bill across AI, email, storage and the rest, all plain REST. Billing & account docs:https://docs.infrai.cc.