package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	awsSession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"
)

var (
	// Environment variables
	ordersTableName = os.Getenv("ORDERS_TABLE_NAME")
	stripeSecretKey = os.Getenv("STRIPE_SECRET_KEY")

	// Constants
	activeStatus = 1

	// AWS clients
	sess         = awsSession.Must(awsSession.NewSession())
	dynamoClient = dynamodb.New(sess)
)

type Order struct {
	OrderID   string    `json:"order_id"`
	UserHash  string    `json:"user_hash"`
	ItemID    string    `json:"item_id"`
	Amount    int64     `json:"amount"`
	Active    int       `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	StripeID  string    `json:"stripe_id,omitempty"`
}

type PaymentVerifyRequest struct {
	OrderID string `json:"order_id"`
}

type PaymentVerifyResponse struct {
	Success   bool   `json:"success"`
	OrderID   string `json:"order_id,omitempty"`
	ProductID string `json:"product_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

func init() {
	// Set Stripe API key
	stripe.Key = stripeSecretKey

	// Validate required environment variables
	if ordersTableName == "" || stripeSecretKey == "" {
		log.Fatal("Required environment variables are not set")
	}
}

func createResponse(statusCode int, body any) events.APIGatewayProxyResponse {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		log.Printf("Error marshalling response body: %v", err)
		return events.APIGatewayProxyResponse{
			StatusCode: http.StatusInternalServerError,
			Body:       `{"success": false, "error": "Internal Server Error"}`,
			Headers: map[string]string{
				"Content-Type":                "application/json",
				"Access-Control-Allow-Origin": "*",
			},
		}
	}
	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Body:       string(jsonBody),
		Headers: map[string]string{
			"Content-Type":                "application/json",
			"Access-Control-Allow-Origin": "*",
		},
	}
}

func getOrderByStripeID(ctx context.Context, stripeID string) (*Order, error) {
	// Query using a GSI on StripeID
	input := &dynamodb.QueryInput{
		TableName:              aws.String(ordersTableName),
		IndexName:              aws.String("StripeIdIndex"), // Ensure this GSI exists
		KeyConditionExpression: aws.String("stripe_id = :stripeId"),
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":stripeId": {S: aws.String(stripeID)},
		},
	}

	result, err := dynamoClient.QueryWithContext(ctx, input)
	if err != nil {
		log.Printf("Failed to query orders by stripe ID: %v", err)
		return nil, errors.New("internal server error")
	}

	if len(result.Items) == 0 {
		return nil, errors.New("order not found")
	}

	var order Order
	err = dynamodbattribute.UnmarshalMap(result.Items[0], &order)
	if err != nil {
		log.Printf("Failed to unmarshal order data: %v", err)
		return nil, errors.New("internal server error")
	}

	return &order, nil
}

func activateOrder(ctx context.Context, order *Order) error {
	now := time.Now()

	// Update only the fields we need to change
	input := &dynamodb.UpdateItemInput{
		TableName: aws.String(ordersTableName),
		Key: map[string]*dynamodb.AttributeValue{
			"order_id": {S: aws.String(order.OrderID)},
		},
		UpdateExpression: aws.String("SET active = :active, updated_at = :updatedAt"),
		ConditionExpression: aws.String(
			"attribute_not_exists(#updatedAt) OR #updatedAt = :isNull",
		),
		ExpressionAttributeNames: map[string]*string{
			"#updatedAt": aws.String("updated_at"),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":active":    {N: aws.String("1")},
			":updatedAt": {S: aws.String(now.Format(time.RFC3339))},
			":isNull":    {NULL: aws.Bool(true)},
		},
		ReturnValues: aws.String("NONE"),
	}

	_, err := dynamoClient.UpdateItemWithContext(ctx, input)
	if err != nil {
		log.Printf("Failed to activate order in DynamoDB: %v", err)
		return errors.New("internal server error")
	}

	return nil
}

func handlePaymentVerification(
	ctx context.Context,
	request events.APIGatewayProxyRequest,
) (events.APIGatewayProxyResponse, error) {
	requestID := request.RequestContext.RequestID
	log.Printf("[%s] Processing payment verification request", requestID)

	var verifyRequest PaymentVerifyRequest
	if err := json.Unmarshal([]byte(request.Body), &verifyRequest); err != nil {
		log.Printf("[%s] Failed to parse verify request body: %v", requestID, err)
		return createResponse(http.StatusBadRequest, PaymentVerifyResponse{
			Success: false,
			Error:   "Invalid request format",
		}), nil
	}

	if verifyRequest.OrderID == "" {
		log.Printf("[%s] Order ID is missing in request", requestID)
		return createResponse(http.StatusBadRequest, PaymentVerifyResponse{
			Success: false,
			Error:   "Order ID is required",
		}), nil
	}

	// The order ID from the success URL is the Stripe Session ID
	stripeSessionID := verifyRequest.OrderID
	log.Printf("[%s] Looking up order with Stripe session ID: %s", requestID, stripeSessionID)

	// Get order by Stripe session ID
	order, err := getOrderByStripeID(ctx, stripeSessionID)
	if err != nil {
		log.Printf("[%s] Failed to find order: %v", requestID, err)
		return createResponse(http.StatusNotFound, PaymentVerifyResponse{
			Success: false,
			Error:   "Order not found",
		}), nil
	}

	// Check if order is already activated
	if order.Active == activeStatus {
		log.Printf("[%s] Order %s already activated", requestID, order.OrderID)
		return createResponse(http.StatusOK, PaymentVerifyResponse{
			Success:   true,
			OrderID:   order.OrderID,
			ProductID: order.ItemID,
		}), nil
	}

	// Verify payment with Stripe
	log.Printf("[%s] Verifying payment with Stripe for session ID: %s", requestID, stripeSessionID)
	sess, err := session.Get(stripeSessionID, nil)
	if err != nil {
		log.Printf("[%s] Failed to get Stripe session: %v", requestID, err)
		return createResponse(http.StatusInternalServerError, PaymentVerifyResponse{
			Success: false,
			Error:   "Failed to verify payment with Stripe",
		}), nil
	}

	if sess.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		log.Printf("[%s] Payment not completed. Status: %s", requestID, sess.PaymentStatus)
		return createResponse(http.StatusBadRequest, PaymentVerifyResponse{
			Success: false,
			Error:   "Payment not completed",
		}), nil
	}

	// Activate the order
	log.Printf("[%s] Activating order %s", requestID, order.OrderID)
	err = activateOrder(ctx, order)
	if err != nil {
		log.Printf("[%s] Failed to activate order: %v", requestID, err)
		return createResponse(http.StatusInternalServerError, PaymentVerifyResponse{
			Success: false,
			Error:   "Failed to activate order",
		}), nil
	}

	log.Printf("[%s] Successfully verified and activated order %s", requestID, order.OrderID)
	return createResponse(http.StatusOK, PaymentVerifyResponse{
		Success:   true,
		OrderID:   order.OrderID,
		ProductID: order.ItemID,
	}), nil
}

func handleRequest(
	ctx context.Context,
	request events.APIGatewayProxyRequest,
) (events.APIGatewayProxyResponse, error) {
	// Handle OPTIONS requests for CORS
	if request.HTTPMethod == "OPTIONS" {
		return events.APIGatewayProxyResponse{
			StatusCode: http.StatusOK,
			Headers: map[string]string{
				"Access-Control-Allow-Origin":  "*",
				"Access-Control-Allow-Methods": "POST, OPTIONS",
				"Access-Control-Allow-Headers": "Content-Type, Authorization",
			},
		}, nil
	}

	// Only handle POST requests to /payments/verify endpoint
	if request.HTTPMethod == "POST" && request.Path == "/payments/verify" {
		return handlePaymentVerification(ctx, request)
	}

	// Return 404 for any other request
	return createResponse(http.StatusNotFound, map[string]any{
		"success": false,
		"error":   "Not Found",
	}), nil
}

func main() {
	lambda.Start(handleRequest)
}
