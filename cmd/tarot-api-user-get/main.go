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
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsSession "github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
)

var (
	// Environment variables for configuration
	authTableName            = os.Getenv("AUTH_TABLE_NAME")
	usersTableName           = os.Getenv("USERS_TABLE_NAME")
	ordersTableName          = os.Getenv("ORDERS_TABLE_NAME")
	productsTableName        = os.Getenv("PRODUCTS_TABLE_NAME")
	defaultRequestsEnv       = os.Getenv("DEFAULT_REQUESTS")
	defaultRefillAmountEnv   = os.Getenv("DEFAULT_REFILL_AMOUNT")
	defaultRefillIntervalEnv = os.Getenv("DEFAULT_REFILL_INTERVAL")

	defaultRequests       = 5
	defaultRefillAmount   = 0
	defaultRefillInterval = 0 // in hours, 0 means no refill
	activeStatus          = 1
	inactiveStatus        = 0

	// AWS session and DynamoDB client
	sess         = awsSession.Must(awsSession.NewSession())
	dynamoClient = dynamodb.New(sess)
)

type User struct {
	UserHash          string    `json:"user_hash"`
	RemainingRequests int       `json:"remaining_requests"`
	NextRefillTime    time.Time `json:"next_refill_time"`
	RefillInterval    int       `json:"refill_interval"` // in hours, 0 means no refill
	RefillAmount      int       `json:"refill_amount"`
}

type UserDataResponse struct {
	RemainingRequests int        `json:"remaining_requests"`
	NextRefillTime    *time.Time `json:"next_refill_time,omitempty"`
}

type UserResponse struct {
	Success bool              `json:"success"`
	Data    *UserDataResponse `json:"data,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func init() {
	// Initialize default values from environment variables
	if v, err := strconv.Atoi(defaultRequestsEnv); err == nil {
		defaultRequests = v
	}
	if v, err := strconv.Atoi(defaultRefillAmountEnv); err == nil {
		defaultRefillAmount = v
	}
	if v, err := strconv.Atoi(defaultRefillIntervalEnv); err == nil {
		defaultRefillInterval = v
	}

	// Ensure that table names are provided
	if authTableName == "" || usersTableName == "" || ordersTableName == "" || productsTableName == "" {
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

func getProductTokensBatch(ctx context.Context, productNumbers []string) (int, error) {
	if len(productNumbers) == 0 {
		return 0, nil
	}

	keys := []map[string]*dynamodb.AttributeValue{}
	for _, productNumber := range productNumbers {
		keys = append(keys, map[string]*dynamodb.AttributeValue{
			"product_number": {S: awsString(productNumber)},
		})
	}

	requestItems := map[string]*dynamodb.KeysAndAttributes{
		productsTableName: {
			Keys:                 keys,
			ProjectionExpression: awsString("tokens"),
		},
	}

	batchInput := &dynamodb.BatchGetItemInput{
		RequestItems: requestItems,
	}

	result, err := dynamoClient.BatchGetItemWithContext(ctx, batchInput)
	if err != nil {
		log.Printf("Failed to batch get items from PRODUCTS table: %v", err)
		return 0, errors.New("internal server error")
	}

	totalTokens := 0
	for _, item := range result.Responses[productsTableName] {
		var product struct {
			Tokens int `json:"tokens"`
		}
		err := dynamodbattribute.UnmarshalMap(item, &product)
		if err != nil {
			log.Printf("Failed to unmarshal product tokens: %v", err)
			continue
		}
		totalTokens += product.Tokens
	}

	return totalTokens, nil
}

func getUnprocessedOrdersAndProducts(ctx context.Context, userHash string) ([]string, []string, error) {
	input := &dynamodb.QueryInput{
		TableName:              awsString(ordersTableName),
		IndexName:              awsString("UserHashActiveIndex"), // Ensure GSI exists
		KeyConditionExpression: awsString("user_hash = :userHash AND active = :active"),
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":userHash": {S: awsString(userHash)},
			":active":   {N: awsString(strconv.Itoa(activeStatus))},
		},
		ProjectionExpression: awsString("order_id, item_id"),
	}

	var orderNumbers, productNumbers []string
	err := dynamoClient.QueryPagesWithContext(ctx, input, func(page *dynamodb.QueryOutput, lastPage bool) bool {
		for _, item := range page.Items {
			orderID := item["order_id"].S
			itemID := item["item_id"].S
			if orderID != nil && itemID != nil {
				orderNumbers = append(orderNumbers, *orderID)
				productNumbers = append(productNumbers, *itemID)
			}
		}
		return true
	})

	if err != nil {
		log.Printf("Failed to query DynamoDB: %v", err)
		return nil, nil, errors.New("internal server error")
	}

	return orderNumbers, productNumbers, nil
}

func markOrdersAsProcessed(ctx context.Context, orderNumbers []string) error {
	if len(orderNumbers) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errorChan := make(chan error, len(orderNumbers))

	for _, orderNumber := range orderNumbers {
		wg.Add(1)
		go func(orderID string) {
			defer wg.Done()
			input := &dynamodb.UpdateItemInput{
				TableName: awsString(ordersTableName),
				Key: map[string]*dynamodb.AttributeValue{
					"order_id": {S: awsString(orderID)},
				},
				UpdateExpression:          awsString("SET active = :inactive"),
				ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{":inactive": {N: awsString(strconv.Itoa(inactiveStatus))}},
			}

			_, err := dynamoClient.UpdateItemWithContext(ctx, input)
			if err != nil {
				log.Printf("Failed to mark order %s as inactive: %v", orderID, err)
				errorChan <- err
			}
		}(orderNumber)
	}

	wg.Wait()
	close(errorChan)

	if len(errorChan) > 0 {
		return errors.New("failed to update some orders")
	}

	return nil
}

func getUser(ctx context.Context, key string) (events.APIGatewayProxyResponse, error) {
	requestID := ctx.Value("requestID")
	if key == "" {
		log.Printf("[%v] Invalid key provided", requestID)
		response := UserResponse{Success: false, Error: "Invalid key"}
		return createResponse(http.StatusBadRequest, response), nil
	}

	// Query AUTH table
	authResult, err := dynamoClient.GetItemWithContext(ctx, &dynamodb.GetItemInput{
		TableName: awsString(authTableName),
		Key:       map[string]*dynamodb.AttributeValue{"key": {S: awsString(key)}},
	})
	if err != nil {
		log.Printf("[%v] Failed to query AUTH table: %v", requestID, err)
		response := UserResponse{Success: false, Error: "Internal server error"}
		return createResponse(http.StatusInternalServerError, response), nil
	}

	if authResult.Item == nil {
		response := UserResponse{Success: false, Error: "User not found"}
		return createResponse(http.StatusNotFound, response), nil
	}

	userHashAttr, ok := authResult.Item["user_hash"]
	if !ok || userHashAttr.S == nil {
		log.Printf("[%v] UserHash not found in AUTH table for key: %s", requestID, key)
		response := UserResponse{Success: false, Error: "Invalid user data"}
		return createResponse(http.StatusInternalServerError, response), nil
	}
	userHash := *userHashAttr.S

	// Query USERS table
	userResult, err := dynamoClient.GetItemWithContext(ctx, &dynamodb.GetItemInput{
		TableName: awsString(usersTableName),
		Key:       map[string]*dynamodb.AttributeValue{"user_hash": {S: awsString(userHash)}},
	})
	if err != nil {
		log.Printf("[%v] Failed to query USERS table: %v", requestID, err)
		response := UserResponse{Success: false, Error: "Internal server error"}
		return createResponse(http.StatusInternalServerError, response), nil
	}

	var user User
	currentTime := time.Now()
	if userResult.Item != nil {
		err = dynamodbattribute.UnmarshalMap(userResult.Item, &user)
		if err != nil {
			log.Printf("[%v] Failed to unmarshal user data: %v", requestID, err)
			response := UserResponse{Success: false, Error: "Internal server error"}
			return createResponse(http.StatusInternalServerError, response), nil
		}
	} else {
		// Create new user with default values
		user = User{
			UserHash:          userHash,
			RemainingRequests: defaultRequests,
			NextRefillTime:    currentTime.Add(time.Duration(defaultRefillInterval) * time.Hour),
			RefillInterval:    defaultRefillInterval,
			RefillAmount:      defaultRefillAmount,
		}
	}

	// Handle refill logic
	if user.RefillInterval > 0 && currentTime.After(user.NextRefillTime) {
		user.RemainingRequests = user.RefillAmount
		user.NextRefillTime = currentTime.Add(time.Duration(user.RefillInterval) * time.Hour)
	}

	// Process unprocessed orders
	orders, products, err := getUnprocessedOrdersAndProducts(ctx, userHash)
	if err != nil {
		response := UserResponse{Success: false, Error: "Internal server error"}
		return createResponse(http.StatusInternalServerError, response), nil
	}

	// Use BatchGetItem for products
	tokens, err := getProductTokensBatch(ctx, products)
	if err != nil {
		response := UserResponse{Success: false, Error: "Internal server error"}
		return createResponse(http.StatusInternalServerError, response), nil
	}

	if tokens > 0 {
		user.RemainingRequests += tokens
		err := markOrdersAsProcessed(ctx, orders)
		if err != nil {
			response := UserResponse{Success: false, Error: "Internal server error"}
			return createResponse(http.StatusInternalServerError, response), nil
		}
	}

	// Update user record
	userItem, err := dynamodbattribute.MarshalMap(user)
	if err != nil {
		log.Printf("[%v] Failed to marshal user data: %v", requestID, err)
		response := UserResponse{Success: false, Error: "Internal server error"}
		return createResponse(http.StatusInternalServerError, response), nil
	}

	_, err = dynamoClient.PutItemWithContext(ctx, &dynamodb.PutItemInput{
		TableName: awsString(usersTableName),
		Item:      userItem,
	})
	if err != nil {
		log.Printf("[%v] Failed to update user in DynamoDB: %v", requestID, err)
		response := UserResponse{Success: false, Error: "Internal server error"}
		return createResponse(http.StatusInternalServerError, response), nil
	}

	// Prepare response
	userDataResponse := UserDataResponse{
		RemainingRequests: user.RemainingRequests,
	}

	if user.RefillInterval > 0 {
		userDataResponse.NextRefillTime = &user.NextRefillTime
	}

	response := UserResponse{
		Success: true,
		Data:    &userDataResponse,
	}

	return createResponse(http.StatusOK, response), nil
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Generate a request ID for logging
	requestID := request.RequestContext.RequestID
	ctx = context.WithValue(ctx, "requestID", requestID)

	// Remove trailing slash from path
	path := strings.TrimSuffix(request.Path, "/")

	switch {
	case request.HTTPMethod == "GET" && strings.HasPrefix(path, "/users/"):
		key := strings.TrimPrefix(path, "/users/")
		return getUser(ctx, key)
	default:
		log.Printf("[%v] Unknown endpoint: %s %s", requestID, request.HTTPMethod, request.Path)
		response := UserResponse{Success: false, Error: "Not Found"}
		return createResponse(http.StatusNotFound, response), nil
	}
}

func main() {
	lambda.Start(handleRequest)
}

func awsString(value string) *string {
	return &value
}
