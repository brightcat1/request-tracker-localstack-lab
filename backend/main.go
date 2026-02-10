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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

type CreateRequestInput struct {
	Title string `json:"title"`
}

type CreateRequestOutput struct {
	RequestID string `json:"requestId"`
	Title     string `json:"title"`
	CreatedAt  string `json:"createdAt"`
	TrackingURL string `json:"trackingUrl"`
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

func main() {
	if os.Getenv("APP_ENV") != "production" {
		_ = godotenv.Load(".env")
	}
	ctx := context.Background()
	ddb, err := newDynamoClient(ctx)
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
				"PK":     &types.AttributeValueMemberS{Value: pk},
				"title":  &types.AttributeValueMemberS{Value: out.Title},
				"status": &types.AttributeValueMemberS{Value: "PENDING"},
				"createdAt": &types.AttributeValueMemberS{Value: createdAt},
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

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
