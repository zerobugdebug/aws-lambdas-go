package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	awsSession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
)

var (
	// Environment variables
	paymentsTableName = os.Getenv("PAYMENTS_TABLE_NAME")

	// AWS session and DynamoDB client
	sess         = awsSession.Must(awsSession.NewSession())
	dynamoClient = dynamodb.New(sess)
)

type contextKey string

type PaymentStatus struct {
	Success   bool   `json:"success"`
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type Payment struct {
	PaymentID string    `json:"payment_id"`
	UserID    string    `json:"user_id"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func init() {
	// Ensure that table names are provided
	if paymentsTableName == "" {
		log.Fatal("Table names must be set in environment variables")
	}
}

func createResponse(statusCode int, body interface{}) events.APIGatewayProxyResponse {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		log.Printf("Error marshalling response body: %v", err)
		return events.APIGatewayProxyResponse{
			StatusCode: http.StatusInternalServerError,
			Body:       `{"success": false, "error": "Internal Server Error"}`,
			Headers:    map[string]string{"Content-Type": "application/json"},
		}
	}
	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Body:       string(jsonBody),
		Headers:    map[string]string{"Content-Type": "application/json"},
	}
}

func getPaymentStatus(ctx context.Context, paymentID string) (*PaymentStatus, error) {
	// Query the payment from DynamoDB
	getInput := &dynamodb.GetItemInput{
		TableName: aws.String(paymentsTableName),
		Key: map[string]*dynamodb.AttributeValue{
			"payment_id": {
				S: aws.String(paymentID),
			},
		},
	}

	result, err := dynamoClient.GetItemWithContext(ctx, getInput)
	if err != nil {
		log.Printf("Failed to get payment: %v", err)
		return nil, errors.New("failed to get payment")
	}

	if result.Item == nil {
		return nil, errors.New("payment not found")
	}

	var payment Payment
	err = dynamodbattribute.UnmarshalMap(result.Item, &payment)
	if err != nil {
		log.Printf("Failed to unmarshal payment: %v", err)
		return nil, errors.New("failed to process payment data")
	}

	return &PaymentStatus{
		Success:   true,
		PaymentID: payment.PaymentID,
		Status:    payment.Status,
	}, nil
}

func handleGetPaymentStatus(ctx context.Context, paymentID string) (events.APIGatewayProxyResponse, error) {
	// Check payment status
	status, err := getPaymentStatus(ctx, paymentID)
	if err != nil {
		log.Printf("Error getting payment status: %v", err)
		return createResponse(http.StatusNotFound, PaymentStatus{
			Success: false,
			Error:   "Payment not found",
		}), nil
	}

	return createResponse(http.StatusOK, status), nil
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	requestID := request.RequestContext.RequestID
	ctx = context.WithValue(ctx, contextKey("requestID"), requestID)

	path := strings.TrimSuffix(request.Path, "/")

	switch {
	case request.HTTPMethod == "GET" && strings.HasPrefix(path, "/payment-status/"):
		paymentID := strings.TrimPrefix(path, "/payment-status/")
		return handleGetPaymentStatus(ctx, paymentID)
	default:
		log.Printf("[%v] Unknown endpoint: %s %s", requestID, request.HTTPMethod, path)
		return createResponse(http.StatusNotFound, PaymentStatus{
			Success: false,
			Error:   "Not Found",
		}), nil
	}
}

func main() {
	lambda.Start(handleRequest)
}
