package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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

)

type OTPVerifyRequest struct {
	Identifier string `json:"identifier"`
	OTP        string `json:"otp"`
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

func generateAuthKey() (string, error) {
	bytes := make([]byte, 36) // 128 bits
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

func verifyOTP(request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var verifyReq OTPVerifyRequest
	err := json.Unmarshal([]byte(request.Body), &verifyReq)
	if err != nil {
		return createResponse(http.StatusBadRequest, "Invalid request body"), fmt.Errorf("failed to unmarshal request: %w", err)
	}

	fmt.Printf("verifyReq: %+v\n", verifyReq)
	sess := session.Must(session.NewSession())
	dynamoClient := dynamodb.New(sess)

	result, err := dynamoClient.Query(&dynamodb.QueryInput{
		TableName:              aws.String("OTP"),
		KeyConditionExpression: aws.String("Identifier = :id"),
		FilterExpression:       aws.String("Active = :active"),
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":id":     {S: aws.String(verifyReq.Identifier)},
			":active": {BOOL: aws.Bool(true)},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int64(1),
	})

	if err != nil {
		return createResponse(http.StatusInternalServerError, "Failed to retrieve OTP"), fmt.Errorf("failed to query DynamoDB: %w", err)
	}

	if len(result.Items) == 0 {
		return createResponse(http.StatusBadRequest, "No OTP found"), fmt.Errorf("no OTP found for identifier: %s", verifyReq.Identifier)
	}

	storedOTP := *result.Items[0]["OTP"].S

	if verifyReq.OTP != storedOTP {
		return createResponse(http.StatusBadRequest, "Invalid OTP"), fmt.Errorf("invalid OTP provided for identifier: %s", verifyReq.Identifier)
	}

	// Update Active to false
	_, err = dynamoClient.UpdateItem(&dynamodb.UpdateItemInput{
		TableName: aws.String("OTP"),
		Key: map[string]*dynamodb.AttributeValue{
			"Identifier": {S: aws.String(verifyReq.Identifier)},
		},
		UpdateExpression: aws.String("SET Active = :active"),
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":active": {BOOL: aws.Bool(false)},
		},
	})
	if err != nil {
		return createResponse(http.StatusInternalServerError, "Failed to deactivate OTP"), fmt.Errorf("failed to set Active to false in DynamoDB: %w", err)
	}

	createdAt, _ := strconv.ParseInt(*result.Items[0]["CreatedAt"].N, 10, 64)

	if time.Now().Unix()-createdAt > 300 { // OTP expires after 5 minutes
		return createResponse(http.StatusBadRequest, "OTP expired"), fmt.Errorf("OTP expired for identifier: %s", verifyReq.Identifier)
	}

	// Generate new auth key
	authKey, err := generateAuthKey()
	if err != nil {
		return createResponse(http.StatusInternalServerError, "Failed to generate auth key"), fmt.Errorf("failed to generate auth key: %w", err)
	}

	// Store auth key in DynamoDB
	_, err = dynamoClient.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String("AUTH"),
		Item: map[string]*dynamodb.AttributeValue{
			"key": {S: aws.String(authKey)},
		},
	})
	if err != nil {
		return createResponse(http.StatusInternalServerError, "Failed to store auth key"), fmt.Errorf("failed to store auth key in DynamoDB: %w", err)
	}

	// Return the new auth key
	response := struct {
		Message string `json:"message"`
		AuthKey string `json:"auth_key"`
	}{
		Message: "OTP verified successfully",
		AuthKey: authKey,
	}

	jsonResponse, err := json.Marshal(response)
	if err != nil {
		return createResponse(http.StatusInternalServerError, "Failed to create response"), fmt.Errorf("failed to marshal response: %w", err)
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
