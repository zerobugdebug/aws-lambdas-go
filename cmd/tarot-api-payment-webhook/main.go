package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsSession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/webhook"

)

var (
	// Environment variables
	paymentsTableName     = os.Getenv("PAYMENTS_TABLE_NAME")
	usersTableName        = os.Getenv("USERS_TABLE_NAME")
	stripeWebhookSecret   = os.Getenv("STRIPE_WEBHOOK_SECRET")
	tokenConversionRate   = os.Getenv("TOKEN_CONVERSION_RATE") // Tokens per dollar
	defaultConversionRate = 1

	// AWS session and DynamoDB client
	sess         = awsSession.Must(awsSession.NewSession())
	dynamoClient = dynamodb.New(sess)
)

type contextKey string

type WebhookResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func init() {
	// Initialize token conversion rate
	if rate, err := strconv.Atoi(tokenConversionRate); err == nil {
		defaultConversionRate = rate
	}

	// Ensure that table names are provided
	if paymentsTableName == "" || usersTableName == "" {
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

func updatePaymentStatus(ctx context.Context, paymentID string, status string) error {
	updateInput := &dynamodb.UpdateItemInput{
		TableName: awsString(paymentsTableName),
		Key: map[string]*dynamodb.AttributeValue{
			"payment_id": {S: awsString(paymentID)},
		},
		UpdateExpression: awsString("SET #status = :status, updated_at = :updated_at"),
		ExpressionAttributeNames: map[string]*string{
			"#status": awsString("status"),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":status":     {S: awsString(status)},
			":updated_at": {S: awsString(time.Now().Format(time.RFC3339))},
		},
	}

	_, err := dynamoClient.UpdateItemWithContext(ctx, updateInput)
	if err != nil {
		log.Printf("Failed to update payment status: %v", err)
		return errors.New("failed to update payment status")
	}

	return nil
}

func addTokensToUser(ctx context.Context, userID string, amount int64) error {
	tokens := int(amount/100) * defaultConversionRate // Convert cents to dollars then to tokens

	updateInput := &dynamodb.UpdateItemInput{
		TableName: awsString(usersTableName),
		Key: map[string]*dynamodb.AttributeValue{
			"user_hash": {S: awsString(userID)},
		},
		UpdateExpression: awsString("ADD remaining_tokens :tokens"),
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":tokens": {N: awsString(strconv.Itoa(tokens))},
		},
	}

	_, err := dynamoClient.UpdateItemWithContext(ctx, updateInput)
	if err != nil {
		log.Printf("Failed to update user tokens: %v", err)
		return errors.New("failed to update user tokens")
	}

	return nil
}

func handleWebhook(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	event, err := webhook.ConstructEvent(
		[]byte(request.Body),
		request.Headers["Stripe-Signature"],
		stripeWebhookSecret,
	)
	if err != nil {
		log.Printf("Failed to verify webhook signature: %v", err)
		return createResponse(http.StatusBadRequest, WebhookResponse{
			Success: false,
			Error:   "Invalid webhook signature",
		}), nil
	}

	switch event.Type {
	case "payment_intent.succeeded":
		var paymentIntent stripe.PaymentIntent
		err := json.Unmarshal(event.Data.Raw, &paymentIntent)
		if err != nil {
			log.Printf("Failed to parse payment intent data: %v", err)
			return createResponse(http.StatusBadRequest, WebhookResponse{
				Success: false,
				Error:   "Invalid payment intent data",
			}), nil
		}

		// Update payment status
		err = updatePaymentStatus(ctx, paymentIntent.ID, "succeeded")
		if err != nil {
			return createResponse(http.StatusInternalServerError, WebhookResponse{
				Success: false,
				Error:   "Failed to process payment",
			}), nil
		}

		// Add tokens to user
		userID := paymentIntent.Metadata["userId"]
		err = addTokensToUser(ctx, userID, paymentIntent.Amount)
		if err != nil {
			return createResponse(http.StatusInternalServerError, WebhookResponse{
				Success: false,
				Error:   "Failed to add tokens",
			}), nil
		}

	case "payment_intent.payment_failed":
		var paymentIntent stripe.PaymentIntent
		err := json.Unmarshal(event.Data.Raw, &paymentIntent)
		if err != nil {
			log.Printf("Failed to parse payment intent data: %v", err)
			return createResponse(http.StatusBadRequest, WebhookResponse{
				Success: false,
				Error:   "Invalid payment intent data",
			}), nil
		}

		err = updatePaymentStatus(ctx, paymentIntent.ID, "failed")
		if err != nil {
			return createResponse(http.StatusInternalServerError, WebhookResponse{
				Success: false,
				Error:   "Failed to process payment",
			}), nil
		}
	}

	return createResponse(http.StatusOK, WebhookResponse{
		Success: true,
	}), nil
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	requestID := request.RequestContext.RequestID
	ctx = context.WithValue(ctx, contextKey("requestID"), requestID)

	path := strings.TrimSuffix(request.Path, "/")

	switch {
	case request.HTTPMethod == "POST" && path == "/webhook":
		return handleWebhook(ctx, request)
	default:
		log.Printf("[%v] Unknown endpoint: %s %s", requestID, request.HTTPMethod, path)
		return createResponse(http.StatusNotFound, WebhookResponse{
			Success: false,
			Error:   "Not Found",
		}), nil
	}
}

func main() {
	lambda.Start(handleRequest)
}

func awsString(value string) *string {
	return &value
}
