package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"

)

const (
	authTableName         = "AUTH"
	usersTableName        = "USERS"
	defaultRequests       = 5
	defaultRefillAmount   = 5
	defaultRefillInterval = 0 // 24 hours (daily) by default
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
	Success bool             `json:"success"`
	Data    UserDataResponse `json:"data,omitempty"`
	Error   string           `json:"error,omitempty"`
}

func createResponse(statusCode int, body string) events.APIGatewayProxyResponse {
	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Body:       body,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}
}

func getUser(key string) (events.APIGatewayProxyResponse, error) {
	sess := session.Must(session.NewSession())
	dynamoClient := dynamodb.New(sess)

	// Query AUTH DB to get user_hash
	authResult, err := dynamoClient.GetItem(&dynamodb.GetItemInput{
		TableName: aws.String(authTableName),
		Key: map[string]*dynamodb.AttributeValue{
			"key": {S: aws.String(key)},
		},
	})
	if err != nil {
		fmt.Printf("Failed to query AUTH DB: %v\n", err)
		return createResponse(http.StatusInternalServerError, "Failed to retrieve user"), nil
	}

	var userHash string
	if authResult.Item != nil {
		if userHashAttr, ok := authResult.Item["user_hash"]; ok && userHashAttr.S != nil {
			userHash = *userHashAttr.S
		} else {
			fmt.Printf("UserHash not found or not a string in AUTH DB for key: %s\n", key)
			return createResponse(http.StatusInternalServerError, "Invalid user data"), nil
		}
	} else {

		return createResponse(http.StatusNotFound, "User not found"), nil
	}

	// Query USERS DB based on user_hash
	userResult, err := dynamoClient.GetItem(&dynamodb.GetItemInput{
		TableName: aws.String(usersTableName),
		Key: map[string]*dynamodb.AttributeValue{
			"user_hash": {S: aws.String(userHash)},
		},
	})
	if err != nil {
		fmt.Printf("Failed to query USERS DB: %v\n", err)
		return createResponse(http.StatusInternalServerError, "Failed to retrieve user data"), nil
	}

	var user User
	if userResult.Item != nil {
		err = dynamodbattribute.UnmarshalMap(userResult.Item, &user)
		if err != nil {
			fmt.Printf("Failed to unmarshal user data: %v\n", err)
			return createResponse(http.StatusInternalServerError, "Failed to process user data"), nil
		}
	} else {
		// Create new user record with default values
		now := time.Now()
		user = User{
			UserHash:          userHash,
			RemainingRequests: defaultRequests,
			NextRefillTime:    now.Add(time.Duration(defaultRefillInterval) * time.Hour),
			RefillInterval:    defaultRefillInterval,
			RefillAmount:      defaultRefillAmount,
		}
	}

	// Check if refill is required
	now := time.Now()
	if user.RefillInterval > 0 && now.After(user.NextRefillTime) {
		user.RemainingRequests = user.RefillAmount
		user.NextRefillTime = now.Add(time.Duration(user.RefillInterval) * time.Hour)
	}

	// Update user record in DynamoDB
	updatedUserItem, err := dynamodbattribute.MarshalMap(user)
	if err != nil {
		fmt.Printf("Failed to marshal user data: %v\n", err)
		return createResponse(http.StatusInternalServerError, "Failed to update user data"), nil
	}

	_, err = dynamoClient.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String(usersTableName),
		Item:      updatedUserItem,
	})
	if err != nil {
		fmt.Printf("Failed to update user in DynamoDB: %v\n", err)
		return createResponse(http.StatusInternalServerError, "Failed to update user data"), nil
	}

	// Create UserDataResponse
	userDataResponse := UserDataResponse{
		RemainingRequests: user.RemainingRequests,
	}

	// Only include NextRefillTime if it's not a lifetime request
	if user.RefillInterval > 0 {
		userDataResponse.NextRefillTime = &user.NextRefillTime
	}

	// Create UserResponse
	userResponse := UserResponse{
		Success: true,
		Data:    userDataResponse,
	}
	// Marshal only the UserResponse
	jsonResponse, err := json.Marshal(userResponse)
	if err != nil {
		fmt.Printf("Failed to marshal response: %v\n", err)
		return createResponse(http.StatusInternalServerError, "Failed to create response"), nil
	}

	return createResponse(http.StatusOK, string(jsonResponse)), nil
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Remove trailing slash from path if present
	path := strings.TrimSuffix(request.Path, "/")

	switch {
	case request.HTTPMethod == "GET" && strings.HasPrefix(path, "/users/"):
		key := strings.TrimPrefix(path, "/users/")
		return getUser(key)
	default:
		fmt.Printf("Unknown endpoint: %s %s", request.HTTPMethod, request.Path)
		return createResponse(http.StatusNotFound, "Not Found"), nil
	}
}

func main() {
	lambda.Start(handleRequest)
}
