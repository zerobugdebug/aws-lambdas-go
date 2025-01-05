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
	awsSession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/paymentintent"
)

var (
	// Environment variables
	paymentsTableName = os.Getenv("PAYMENTS_TABLE_NAME")
	usersTableName    = os.Getenv("USERS_TABLE_NAME")
	stripeSecretKey   = os.Getenv("STRIPE_SECRET_KEY")

	// AWS session and DynamoDB client
	sess         = awsSession.Must(awsSession.NewSession())
	dynamoClient = dynamodb.New(sess)
)

type contextKey string

type PaymentRequest struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	UserID   string `json:"userId"`
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

type PaymentResponse struct {
	Success      bool   `json:"success"`
	ClientSecret string `json:"client_secret,omitempty"`
	PaymentID    string `json:"payment_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

func init() {
	// Initialize Stripe
	stripe.Key = stripeSecretKey

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

func createPaymentIntent(request PaymentRequest) (*stripe.PaymentIntent, error) {
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(request.Amount * 100), // Convert to cents
		Currency: stripe.String(request.Currency),

		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
		Metadata: map[string]string{
			"userId": request.UserID,
		},
	}

	return paymentintent.New(params)
}

func storePayment(ctx context.Context, pi *stripe.PaymentIntent, userID string) error {
	payment := Payment{
		PaymentID: pi.ID,
		UserID:    userID,
		Amount:    pi.Amount,
		Currency:  string(pi.Currency),
		Status:    "pending",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	item, err := dynamodbattribute.MarshalMap(payment)
	if err != nil {
		log.Printf("Failed to marshal payment data: %v", err)
		return errors.New("failed to process payment data")
	}

	_, err = dynamoClient.PutItemWithContext(ctx, &dynamodb.PutItemInput{
		TableName: awsString(paymentsTableName),
		Item:      item,
	})

	if err != nil {
		log.Printf("Failed to store payment in DynamoDB: %v", err)
		return errors.New("failed to store payment")
	}

	return nil
}

func createPayment(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var paymentReq PaymentRequest
	if err := json.Unmarshal([]byte(request.Body), &paymentReq); err != nil {
		log.Printf("Failed to unmarshal request body: %v", err)
		return createResponse(http.StatusBadRequest, PaymentResponse{
			Success: false,
			Error:   "Invalid request body",
		}), nil
	}

	// Create Stripe payment intent
	pi, err := createPaymentIntent(paymentReq)
	if err != nil {
		log.Printf("Failed to create payment intent: %v", err)
		return createResponse(http.StatusInternalServerError, PaymentResponse{
			Success: false,
			Error:   "Failed to create payment",
		}), nil
	}

	// Store payment details
	err = storePayment(ctx, pi, paymentReq.UserID)
	if err != nil {
		return createResponse(http.StatusInternalServerError, PaymentResponse{
			Success: false,
			Error:   "Failed to process payment",
		}), nil
	}

	return createResponse(http.StatusOK, PaymentResponse{
		Success:      true,
		ClientSecret: pi.ClientSecret,
		PaymentID:    pi.ID,
	}), nil
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	requestID := request.RequestContext.RequestID
	ctx = context.WithValue(ctx, contextKey("requestID"), requestID)

	path := strings.TrimSuffix(request.Path, "/")

	switch {
	case request.HTTPMethod == "POST" && path == "/create-payment-intent":
		return createPayment(ctx, request)
	default:
		log.Printf("[%v] Unknown endpoint: %s %s", requestID, request.HTTPMethod, path)
		return createResponse(http.StatusNotFound, PaymentResponse{
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
