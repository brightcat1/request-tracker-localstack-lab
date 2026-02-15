package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

const (
	requestsTable = "Requests"
	queueName     = "request-events"
)

type CreateRequestInput struct {
	Title string `json:"title"`
}

type CreateRequestOutput struct {
	RequestID   string `json:"requestId"`
	Title       string `json:"title"`
	CreatedAt   string `json:"createdAt"`
	TrackingURL string `json:"trackingUrl"`
}

type GetRequestOutput struct {
	RequestID string `json:"requestId"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

type PatchStatusInput struct {
	Status string `json:"status"`
}

type PatchStatusOutput struct {
	RequestID string `json:"requestId"`
	NewStatus string `json:"newStatus"`
	ChangedAt string `json:"changedAt"`
	EventID   string `json:"eventId"`
}

type StatusChangedEvent struct {
	EventID   string `json:"eventId"`
	RequestID string `json:"requestId"`
	NewStatus string `json:"newStatus"`
	ChangedAt string `json:"changedAt"`
}

func newDynamoClient(ctx context.Context) (*dynamodb.Client, error) {
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("DYNAMODB_ENDPOINT is required")
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(os.Getenv("AWS_REGION")),
	)
	if err != nil {
		return nil, err
	}

	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	}), nil
}

func newSQSClient(ctx context.Context) (*sqs.Client, error) {
	endpoint := os.Getenv("SQS_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("SQS_ENDPOINT is required")
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(os.Getenv("AWS_REGION")))
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	}), nil
}

func resolveQueueURL(ctx context.Context, c *sqs.Client) (string, error) {
    if v := os.Getenv("SQS_QUEUE_URL"); v != "" {
        return v, nil
    }
    out, err := c.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{
        QueueName: aws.String(queueName),
    })
    if err != nil {
        return "", err
    }
    return aws.ToString(out.QueueUrl), nil
}

func getStringAttr(item map[string]types.AttributeValue, key string) (string, bool) {
	v, ok := item[key].(*types.AttributeValueMemberS)
	if !ok {
		return "", false
	}
	return v.Value, true
}

func main() {

	if os.Getenv("APP_ENV") != "production" {
		_ = godotenv.Load(".env")
	}
	ctx := context.Background()

	ddb, err := newDynamoClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	sqsClient, err := newSQSClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	queueURL, err := resolveQueueURL(ctx, sqsClient)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/requests", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var in CreateRequestInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if in.Title == "" {
			http.Error(w, "title required", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		createdAt := time.Now().UTC().Format(time.RFC3339)
		out := CreateRequestOutput{
			RequestID: uuid.NewString(),
			Title:     in.Title,
			CreatedAt: createdAt,
		}

		requesterToken := uuid.NewString()

		base := os.Getenv("APP_PUBLIC_BASE_URL")
		if base == "" {
			base = "http://localhost:8080"
		}
		base = strings.TrimRight(base, "/")
		out.TrackingURL = fmt.Sprintf("%s/requests/%s?t=%s", base, out.RequestID, requesterToken)

		pk := "REQ#" + out.RequestID
		reqCtx := r.Context()
		_, err = ddb.PutItem(reqCtx, &dynamodb.PutItemInput{
			TableName: aws.String("Requests"),
			Item: map[string]types.AttributeValue{
				"PK":             &types.AttributeValueMemberS{Value: pk},
				"title":          &types.AttributeValueMemberS{Value: out.Title},
				"status":         &types.AttributeValueMemberS{Value: "PENDING"},
				"createdAt":      &types.AttributeValueMemberS{Value: createdAt},
				"requesterToken": &types.AttributeValueMemberS{Value: requesterToken},
			},
		})
		if err != nil {
			http.Error(w, "failed to persist request", http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			http.Error(w, "failed to write response", http.StatusInternalServerError)
			return
		}
	})
	
	mux.HandleFunc("/requests/", func(w http.ResponseWriter, r *http.Request) {
		// /requests/{id} or /requests/{id}/status
		rest := strings.TrimPrefix(r.URL.Path, "/requests/")
		rest = strings.Trim(rest, "/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		pk := "REQ#" + id

		// ===== GET /requests/{id}?t=... =====
		if len(parts) == 1 && r.Method == http.MethodGet {
			t := r.URL.Query().Get("t")
			if t == "" {
				http.Error(w, "token required", http.StatusBadRequest)
				return
			}

			out, err := ddb.GetItem(r.Context(), &dynamodb.GetItemInput{
				TableName:      aws.String("Requests"),
				Key:            map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: pk}},
				ConsistentRead: aws.Bool(true),
			})
			if err != nil {
				http.Error(w, "failed to read", http.StatusInternalServerError)
				return
			}
			if len(out.Item) == 0 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}

			stored, ok := getStringAttr(out.Item, "requesterToken")
			if !ok {
				http.Error(w, "corrupt item", http.StatusInternalServerError)
				return
			}
			if stored != t {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			title, _ := getStringAttr(out.Item, "title")
			status, _ := getStringAttr(out.Item, "status")
			createdAt, _ := getStringAttr(out.Item, "createdAt")

			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(GetRequestOutput{
				RequestID: id,
				Title:     title,
				Status:    status,
				CreatedAt: createdAt,
			})
			return
		}

		// ===== PATCH /requests/{id}/status (admin only) =====
		if len(parts) == 2 && parts[1] == "status" && r.Method == http.MethodPatch {
			expected := os.Getenv("ADMIN_TOKEN")
			if expected == "" {
				expected = "dev-admin-token"
			}
			if r.Header.Get("Authorization") != "Bearer "+expected {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			var in PatchStatusInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			switch in.Status {
			case "PENDING", "IN_PROGRESS", "DONE", "REJECTED":
			default:
				http.Error(w, "invalid status", http.StatusBadRequest)
				return
			}

			changedAt := time.Now().UTC().Format(time.RFC3339)
			eventID := uuid.NewString()

			// DynamoDB更新（存在しないIDなら404にしたいのでCondition入れる）
			_, err := ddb.UpdateItem(r.Context(), &dynamodb.UpdateItemInput{
				TableName: aws.String("Requests"),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: pk},
				},
				UpdateExpression: aws.String("SET #st = :s, statusUpdatedAt = :t"),
				ExpressionAttributeNames: map[string]string{
					"#st": "status",
				},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":s": &types.AttributeValueMemberS{Value: in.Status},
					":t": &types.AttributeValueMemberS{Value: changedAt},
				},
				ConditionExpression: aws.String("attribute_exists(PK)"),
			})
			if err != nil {
				var cfe *types.ConditionalCheckFailedException
				if errors.As(err, &cfe) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				http.Error(w, "failed to update", http.StatusInternalServerError)
				return
			}

			// SQSへイベント投入（workerが拾って履歴/通知済み等を更新する想定）
			ev := StatusChangedEvent{
				EventID:   eventID,
				RequestID: id,
				NewStatus: in.Status,
				ChangedAt: changedAt,
			}
			body, _ := json.Marshal(ev)
			_, err = sqsClient.SendMessage(r.Context(), &sqs.SendMessageInput{
				QueueUrl:    aws.String(queueURL),
				MessageBody: aws.String(string(body)),
			})
			if err != nil {
				http.Error(w, "failed to enqueue", http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(PatchStatusOutput{
				RequestID: id,
				NewStatus: in.Status,
				ChangedAt: changedAt,
				EventID:   eventID,
			})
			return
		}

		http.NotFound(w, r)
	})

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
