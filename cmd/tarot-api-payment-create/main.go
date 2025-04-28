package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"
)

var (
	// Environment variables
	authTableName     = os.Getenv("AUTH_TABLE_NAME")
	ordersTableName   = os.Getenv("ORDERS_TABLE_NAME")
	productsTableName = os.Getenv("PRODUCTS_TABLE_NAME")
	stripeSecretKey   = os.Getenv("STRIPE_SECRET_KEY")
	successURL        = os.Getenv("SUCCESS_URL")
	cancelURL         = os.Getenv("CANCEL_URL")

	// Constants
	activeStatus = 0 // Initialize as inactive

	// AWS clients
	sess         = awsSession.Must(awsSession.NewSession())
	dynamoClient = dynamodb.New(sess)
)

type Product struct {
	ProductNumber string `json:"product_number"`
	Name          string `json:"name"`
	Price         int64  `json:"price"`
	Tokens        int    `json:"tokens"`
}

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

type PaymentInitRequest struct {
	ProductID string `json:"product_id"`
}

type PaymentInitResponse struct {
	Success     bool   `json:"success"`
	CheckoutURL string `json:"checkout_url,omitempty"`
	OrderID     string `json:"order_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

func init() {
	// Set Stripe API key
	stripe.Key = stripeSecretKey

	// Validate required environment variables
	if authTableName == "" || ordersTableName == "" || productsTableName == "" ||
		stripeSecretKey == "" || successURL == "" || cancelURL == "" {
		log.Fatal("Required environment variables are not set")
	}
}

func createResponse(statusCode int, body interface{}) events.APIGatewayProxyResponse {
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

func getUserHashFromAuthKey(ctx context.Context, authKey string) (string, error) {
	result, err := dynamoClient.GetItemWithContext(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(authTableName),
		Key: map[string]*dynamodb.AttributeValue{
			"key": {S: aws.String(authKey)},
		},
	})
	if err != nil {
		log.Printf("Failed to query AUTH table: %v", err)
		return "", errors.New("internal server error")
	}

	if result.Item == nil {
		return "", errors.New("auth key not found")
	}

	userHashAttr, ok := result.Item["user_hash"]
	if !ok || userHashAttr.S == nil {
		return "", errors.New("invalid user data")
	}

	return *userHashAttr.S, nil
}

func getProductDetails(ctx context.Context, productID string) (*Product, error) {
	result, err := dynamoClient.GetItemWithContext(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(productsTableName),
		Key: map[string]*dynamodb.AttributeValue{
			"product_number": {S: aws.String(productID)},
		},
	})
	if err != nil {
		log.Printf("Failed to query PRODUCTS table: %v", err)
		return nil, errors.New("internal server error")
	}

	if result.Item == nil {
		return nil, errors.New("product not found")
	}

	var product Product
	err = dynamodbattribute.UnmarshalMap(result.Item, &product)
	if err != nil {
		log.Printf("Failed to unmarshal product data: %v", err)
		return nil, errors.New("internal server error")
	}

	return &product, nil
}

func createOrder(ctx context.Context, userHash string, product *Product, stripeSessionID string) (string, error) {
	orderID := uuid.New().String()
	now := time.Now()

	order := Order{
		OrderID:   orderID,
		UserHash:  userHash,
		ItemID:    product.ProductNumber,
		Amount:    product.Price,
		Active:    activeStatus, // Inactive until payment is verified
		CreatedAt: now,
		UpdatedAt: now,
		StripeID:  stripeSessionID,
	}

	orderItem, err := dynamodbattribute.MarshalMap(order)
	if err != nil {
		log.Printf("Failed to marshal order data: %v", err)
		return "", errors.New("internal server error")
	}

	_, err = dynamoClient.PutItemWithContext(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ordersTableName),
		Item:      orderItem,
	})
	if err != nil {
		log.Printf("Failed to create order in DynamoDB: %v", err)
		return "", errors.New("internal server error")
	}

	return orderID, nil
}

func handlePaymentCreation(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	requestID := request.RequestContext.RequestID
	log.Printf("[%s] Processing payment creation request", requestID)

	// Extract authentication token
	authToken := request.Headers["Authorization"]
	if authToken == "" {
		log.Printf("[%s] Missing Authorization header", requestID)
		return createResponse(http.StatusUnauthorized, PaymentInitResponse{
			Success: false,
			Error:   "Authentication required",
		}), nil
	}

	// Remove "Bearer " prefix if present
	if len(authToken) > 7 && authToken[:7] == "Bearer " {
		authToken = authToken[7:]
	}

	// Parse request body
	var paymentRequest PaymentInitRequest
	if err := json.Unmarshal([]byte(request.Body), &paymentRequest); err != nil {
		log.Printf("[%s] Failed to parse request body: %v", requestID, err)
		return createResponse(http.StatusBadRequest, PaymentInitResponse{
			Success: false,
			Error:   "Invalid request format",
		}), nil
	}

	// Get user hash from auth key
	userHash, err := getUserHashFromAuthKey(ctx, authToken)
	if err != nil {
		log.Printf("[%s] Failed to get user hash: %v", requestID, err)
		return createResponse(http.StatusUnauthorized, PaymentInitResponse{
			Success: false,
			Error:   "Invalid authentication",
		}), nil
	}

	// Get product details
	product, err := getProductDetails(ctx, paymentRequest.ProductID)
	if err != nil {
		log.Printf("[%s] Failed to get product: %v", requestID, err)
		return createResponse(http.StatusBadRequest, PaymentInitResponse{
			Success: false,
			Error:   "Product not found",
		}), nil
	}

	// Create Stripe checkout session
	params := &stripe.CheckoutSessionParams{
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency: stripe.String("usd"),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name:        stripe.String(product.Name),
						Description: stripe.String(fmt.Sprintf("%d Tarot Tokens", product.Tokens)),
					},
					UnitAmount: stripe.Int64(product.Price * 100), // Convert to cents
				},
				Quantity: stripe.Int64(1),
			},
		},
		Mode:       stripe.String("payment"),
		SuccessURL: stripe.String(fmt.Sprintf("%s?order_id=%s&status=success", successURL, "{CHECKOUT_SESSION_ID}")),
		CancelURL:  stripe.String(cancelURL),
	}

	checkoutSession, err := session.New(params)
	if err != nil {
		log.Printf("[%s] Failed to create Stripe checkout session: %v", requestID, err)
		return createResponse(http.StatusInternalServerError, PaymentInitResponse{
			Success: false,
			Error:   "Failed to create payment session",
		}), nil
	}

	// Create order in DynamoDB
	orderID, err := createOrder(ctx, userHash, product, checkoutSession.ID)
	if err != nil {
		log.Printf("[%s] Failed to create order: %v", requestID, err)
		return createResponse(http.StatusInternalServerError, PaymentInitResponse{
			Success: false,
			Error:   "Failed to create order",
		}), nil
	}

	log.Printf("[%s] Successfully created payment session. OrderID: %s, StripeID: %s",
		requestID, orderID, checkoutSession.ID)

	// Return checkout URL and order ID
	return createResponse(http.StatusOK, PaymentInitResponse{
		Success:     true,
		CheckoutURL: checkoutSession.URL,
		OrderID:     orderID,
	}), nil
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
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

	// Only handle POST requests to /payments/create endpoint
	if request.HTTPMethod == "POST" && request.Path == "/payments/create" {
		return handlePaymentCreation(ctx, request)
	}

	// Return 404 for any other request
	return createResponse(http.StatusNotFound, map[string]interface{}{
		"success": false,
		"error":   "Not Found",
	}), nil
}

func main() {
	lambda.Start(handleRequest)
}
