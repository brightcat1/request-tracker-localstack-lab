package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/joho/godotenv"
)

const (
	requestsTable = "Requests"
	queueName     = "request-events"
)

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
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(os.Getenv("AWS_REGION")))
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

func main() {
	if os.Getenv("APP_ENV") != "production" {
		_ = godotenv.Load(".env")
	}
	ctx := context.Background()

	ddb, err := newDynamoClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	sqsc, err := newSQSClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	queueURL, err := resolveQueueURL(ctx, sqsc)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("worker started. queue=%s", queueURL)

	for {
		resp, err := sqsc.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     10, // long polling
			VisibilityTimeout:   30,
		})
		if err != nil {
			log.Printf("receive error: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}
		if len(resp.Messages) == 0 {
			continue
		}

		for _, m := range resp.Messages {
			if m.Body == nil || m.ReceiptHandle == nil {
				continue
			}

			var ev StatusChangedEvent
			if err := json.Unmarshal([]byte(*m.Body), &ev); err != nil {
				log.Printf("bad message json: %v body=%q", err, *m.Body)
				// 破損メッセージは消す（Labなので割り切り）
				_, _ = sqsc.DeleteMessage(ctx, &sqs.DeleteMessageInput{
					QueueUrl:      aws.String(queueURL),
					ReceiptHandle: m.ReceiptHandle,
				})
				continue
			}

			// DynamoDBに「通知処理済み」っぽい記録を追記
			if err := applyStatusEvent(ctx, ddb, ev); err != nil {
				log.Printf("apply error: %v eventId=%s requestId=%s", err, ev.EventID, ev.RequestID)
				// 失敗時は消さない → visibility timeout後に再試行される
				continue
			}

			// 成功したらキューから削除（再処理防止）
			_, err = sqsc.DeleteMessage(ctx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(queueURL),
				ReceiptHandle: m.ReceiptHandle,
			})
			if err != nil {
				log.Printf("delete error: %v", err)
				continue
			}

			log.Printf("processed eventId=%s requestId=%s newStatus=%s", ev.EventID, ev.RequestID, ev.NewStatus)
		}
	}
}

func applyStatusEvent(ctx context.Context, ddb *dynamodb.Client, ev StatusChangedEvent) error {
	pk := "REQ#" + ev.RequestID
	now := time.Now().UTC().Format(time.RFC3339)

	historyEntry := &types.AttributeValueMemberM{
		Value: map[string]types.AttributeValue{
			"eventId":   &types.AttributeValueMemberS{Value: ev.EventID},
			"newStatus": &types.AttributeValueMemberS{Value: ev.NewStatus},
			"changedAt": &types.AttributeValueMemberS{Value: ev.ChangedAt},
			"handledAt": &types.AttributeValueMemberS{Value: now},
		},
	}

	_, err := ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(requestsTable),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
		},
		// statusHistory に1件append + notifiedAt更新 + lastEventId保存
		UpdateExpression: aws.String(
			"SET notifiedAt = :n, lastEventId = :eid, " +
				"statusHistory = list_append(if_not_exists(statusHistory, :empty), :h)",
		),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":n":     &types.AttributeValueMemberS{Value: now},
			":eid":   &types.AttributeValueMemberS{Value: ev.EventID},
			":empty": &types.AttributeValueMemberL{Value: []types.AttributeValue{}},
			":h":     &types.AttributeValueMemberL{Value: []types.AttributeValue{historyEntry}},
		},
		// 1) requestが存在すること 2) 同じeventIdを二重処理しない（超簡易）
		ConditionExpression: aws.String("attribute_exists(PK) AND (attribute_not_exists(lastEventId) OR lastEventId <> :eid)"),
	})
	if err != nil {
		var cfe *types.ConditionalCheckFailedException
		if errors.As(err, &cfe) {
			// 「存在しない」or「同じeventを再処理」→ Labでは成功扱いにして削除してOK
			return nil
		}
		return err
	}
	return nil
}
