# README.md

# request-tracker-localstack-lab

A minimal LocalStack lab to learn an AWS-style async workflow:
- Go HTTP API (creates/updates requests)
- DynamoDB (stores Requests)
- SQS (event queue)
- Worker (polls SQS, appends "notification processed" history to DynamoDB)

No UI. Everything is verified via curl + logs.

---

## Architecture (What happens)

1) POST /requests
   - Creates a request item in DynamoDB
   - Returns a trackingUrl that allows token-gated GET

2) GET /requests/{id}?t=...
   - Reads request only if the provided token matches what is stored in DynamoDB

3) PATCH /requests/{id}/status (admin)
   - Updates status in DynamoDB
   - Enqueues a StatusChangedEvent into SQS

4) Worker
   - Long-polls SQS
   - Applies the event to DynamoDB:
     - append statusHistory
     - set notifiedAt
     - set lastEventId (simple de-dup)
   - Deletes the SQS message on success

In a real product, the worker would send email/push notifications.
In this lab, we persist "notification processed" info into DynamoDB instead.

---

## Prerequisites

- LocalStack running
- Go
- OpenTofu (tofu)
- AWS CLI (aws)
- make

---

## Setup

### 1) Terraform variables

Create: infra/envs/local/terraform.tfvars

localstack_endpoint = "${YOUR_LOCALSTACK_ENDPOINT}"

---

### 2) App env file

Create: backend/.env

AWS_REGION=${YOUR_AWS_REGION}
AWS_ACCESS_KEY_ID=${YOUR_AWS_ACCESS_KEY_ID}
AWS_SECRET_ACCESS_KEY=${YOUR_AWS_SECRET_ACCESS_KEY}

DYNAMODB_ENDPOINT=${YOUR_DYNAMODB_ENDPOINT}
SQS_ENDPOINT=${YOUR_SQS_ENDPOINT}

# Used by PATCH /requests/{id}/status:
# Authorization: Bearer <ADMIN_TOKEN>
ADMIN_TOKEN=${YOUR_ADMIN_TOKEN}

# Used to build trackingUrl in POST /requests response
APP_PUBLIC_BASE_URL=${YOUR_PUBLIC_BASE_URL}

Note:
- This lab does not have a real "production mode". The code simply loads .env when APP_ENV != "production".
- You can omit APP_ENV entirely.

---

## Commands

Provision / destroy infra:
- make infra-apply
- make infra-destroy

Run processes:
- make run-backend
- make run-worker

---

## Smoke test (from a clean state)

0) Reset Terraform-managed resources (optional but recommended):
make infra-destroy
make infra-apply

1) Start API server (Terminal A):
make run-backend

2) Start worker (Terminal B):
make run-worker

3) Health check (Terminal C):
curl -s http://localhost:8080/health
Expected: ok

4) Create request (Terminal C):
curl -s -X POST http://localhost:8080/requests \
  -H 'Content-Type: application/json' \
  -d '{"title":"test"}'

Expected JSON:
{"requestId":"...","title":"test","createdAt":"...","trackingUrl":"http://.../requests/<id>?t=<token>"}

Copy:
- REQUEST_ID=<requestId>
- TRACKING_URL=<trackingUrl>

5) GET via trackingUrl (Terminal C):
curl -s "<TRACKING_URL>"
Expected:
- status is "PENDING"

6) PATCH status (Terminal C):
curl -s -X PATCH "http://localhost:8080/requests/<REQUEST_ID>/status" \
  -H "Authorization: Bearer ${YOUR_ADMIN_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"status":"IN_PROGRESS"}'

Expected:
{"requestId":"...","newStatus":"IN_PROGRESS","changedAt":"...","eventId":"..."}

Worker (Terminal B) expected log:
processed eventId=... requestId=... newStatus=IN_PROGRESS

7) GET again (Terminal C):
curl -s "<TRACKING_URL>"
Expected:
- status becomes "IN_PROGRESS"

8) DynamoDB item has history (optional):
aws --endpoint-url=${YOUR_LOCALSTACK_ENDPOINT} dynamodb get-item \
  --table-name Requests \
  --key '{"PK":{"S":"REQ#<REQUEST_ID>"}}'

Expected fields include:
- statusHistory (L)
- notifiedAt (S)
- lastEventId (S)
- status (S) = IN_PROGRESS

9) SQS should be empty (optional):
QUEUE_URL=$(aws --endpoint-url=${YOUR_LOCALSTACK_ENDPOINT} sqs get-queue-url \
  --queue-name request-events --query QueueUrl --output text)

aws --endpoint-url=${YOUR_LOCALSTACK_ENDPOINT} sqs receive-message \
  --queue-url "$QUEUE_URL" \
  --max-number-of-messages 1 \
  --wait-time-seconds 1

Expected:
- no messages returned (worker already deleted them)

---

## Notes

- Why SQS long polling?
  The worker waits for messages (WaitTimeSeconds=10) instead of busy polling.

- Why VisibilityTimeout matters?
  If worker fails to process a message and does NOT delete it, SQS makes it visible again after the visibility timeout, enabling retry.

---

## Cleanup

make infra-destroy
