package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"

	"github.com/zerobugdebug/aws-lambdas-go/pkg/cipher"

)

type OTPVerifyRequest struct {
	Identifier string `json:"identifier"`
	OTP        string `json:"otp"`
	Method     string `json:"method"`
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

func verifyOTP(request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var verifyReq OTPVerifyRequest
	err := json.Unmarshal([]byte(request.Body), &verifyReq)
	if err != nil {
		fmt.Printf("failed to unmarshal request: %v", err)
		return createResponse(http.StatusBadRequest, "Invalid request body"), nil
	}

	fmt.Printf("verifyReq: %+v\n", verifyReq)

	key, err := cipher.GenerateIDHash(verifyReq.Identifier, verifyReq.Method)
	if err != nil {
		fmt.Printf("invalid identifier: %v", err)
		return createResponse(http.StatusUnprocessableEntity, "Invalid identifier"), nil
	}

	sess := session.Must(session.NewSession())
	dynamoClient := dynamodb.New(sess)

	result, err := dynamoClient.Query(&dynamodb.QueryInput{
		TableName:              aws.String("OTP"),
		KeyConditionExpression: aws.String("Identifier = :id"),
		FilterExpression:       aws.String("Active = :active"),
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":id":     {S: aws.String(key)},
			":active": {BOOL: aws.Bool(true)},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int64(1),
	})

	if err != nil {
		fmt.Printf("failed to query DynamoDB: %v", err)
		return createResponse(http.StatusInternalServerError, "Failed to retrieve OTP"), nil
	}

	if len(result.Items) == 0 {
		fmt.Printf("no OTP found for identifier: %s", verifyReq.Identifier)
		return createResponse(http.StatusBadRequest, "No OTP found"), nil
	}

	storedOTP := *result.Items[0]["OTP"].S

	if verifyReq.OTP != storedOTP {
		fmt.Printf("invalid OTP provided for identifier: %s", verifyReq.Identifier)
		return createResponse(http.StatusBadRequest, "Invalid OTP"), nil
	}

	// Update Active to false
	_, err = dynamoClient.UpdateItem(&dynamodb.UpdateItemInput{
		TableName: aws.String("OTP"),
		Key: map[string]*dynamodb.AttributeValue{
			"Identifier": {S: aws.String(key)},
		},
		UpdateExpression: aws.String("SET Active = :active"),
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":active": {BOOL: aws.Bool(false)},
		},
	})
	if err != nil {
		fmt.Printf("failed to set Active to false in DynamoDB: %v", err)
		return createResponse(http.StatusInternalServerError, "Failed to deactivate OTP"), nil
	}

	createdAt, _ := strconv.ParseInt(*result.Items[0]["CreatedAt"].N, 10, 64)

	if time.Now().Unix()-createdAt > 300 { // OTP expires after 5 minutes
		fmt.Printf("OTP expired for identifier: %s", verifyReq.Identifier)
		return createResponse(http.StatusBadRequest, "OTP expired"), nil
	}

	// Generate new auth key
	authKey, err := cipher.GenerateAuthKey()
	if err != nil {
		fmt.Printf("failed to generate auth key: %v", err)
		return createResponse(http.StatusInternalServerError, "Failed to generate auth key"), nil
	}

	// Store auth key in DynamoDB
	_, err = dynamoClient.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String("AUTH"),
		Item: map[string]*dynamodb.AttributeValue{
			"key":       {S: aws.String(authKey)},
			"user_hash": {S: aws.String(key)},
		},
	})

	if err != nil {
		fmt.Printf("failed to store auth key in DynamoDB: %v", err)
		return createResponse(http.StatusInternalServerError, "Failed to store auth key"), nil
	}

	// Return the new auth key
	response := struct {
		Success bool `json:"success"`
		Data    struct {
			AuthKey string `json:"auth_key"`
		} `json:"data,omitempty"`
		Error string `json:"error,omitempty"`
	}{
		Success: true,
		Data: struct {
			AuthKey string `json:"auth_key"`
		}{
			AuthKey: authKey,
		},
	}

	jsonResponse, err := json.Marshal(response)
	if err != nil {
		fmt.Printf("failed to unmarshal response: %v", err)
		response := struct {
			Success bool   `json:"success"`
			Error   string `json:"error,omitempty"`
		}{
			Success: false,
			Error:   "Failed to create response",
		}
		jsonResponse, _ := json.Marshal(response)
		return createResponse(http.StatusInternalServerError, string(jsonResponse)), nil
	}

	return createResponse(http.StatusOK, string(jsonResponse)), nil
}

func main() {
	lambda.Start(handleRequest)
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	//fmt.Printf("Full request: %+v", request)

	// Remove trailing slash from path if present
	path := strings.TrimSuffix(request.Path, "/")

	switch {
	case request.HTTPMethod == "POST" && path == "/verify-otp":
		return verifyOTP(request)
	default:
		return createResponse(http.StatusNotFound, "Not Found"), fmt.Errorf("unknown endpoint: %s %s", request.HTTPMethod, request.Path)
	}
}
