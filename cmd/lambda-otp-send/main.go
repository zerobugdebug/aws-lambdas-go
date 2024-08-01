package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/ses"
	"github.com/aws/aws-sdk-go/service/sns"
)

const (
	defaultEmailAddress = "notifications.otp@evacrane.com"
)

type OTPRequest struct {
	Identifier string `json:"identifier"`
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

func generateOTP() string {
	otp, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%06d", otp)
}

func sendOTP(request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var otpReq OTPRequest
	err := json.Unmarshal([]byte(request.Body), &otpReq)
	if err != nil {
		return createResponse(http.StatusBadRequest, "Invalid request body"), fmt.Errorf("failed to unmarshal request: %w", err)
	}
	fmt.Printf("otpReq: %+v\n", otpReq)

	otp := generateOTP()
	fmt.Printf("Generated OTP: %v\n", otp)

	sess := session.Must(session.NewSession())

	// Store OTP in DynamoDB
	dynamoClient := dynamodb.New(sess)
	_, err = dynamoClient.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String("OTP"),
		Item: map[string]*dynamodb.AttributeValue{
			"Identifier": {S: aws.String(otpReq.Identifier)},
			"CreatedAt":  {N: aws.String(strconv.FormatInt(time.Now().Unix(), 10))},
			"OTP":        {S: aws.String(otp)},
			"Active":     {BOOL: aws.Bool(true)},
		},
	})
	if err != nil {
		return createResponse(http.StatusInternalServerError, "Failed to store OTP"), fmt.Errorf("failed to store OTP in DynamoDB: %w", err)
	}

	switch otpReq.Method {
	case "sms":
		snsClient := sns.New(sess)
		_, err = snsClient.Publish(&sns.PublishInput{
			Message:     aws.String(fmt.Sprintf("Your OTP is: %s", otp)),
			PhoneNumber: aws.String(otpReq.Identifier),
		})
	case "email":
		sesClient := ses.New(sess)
		_, err = sesClient.SendEmail(&ses.SendEmailInput{
			Source: aws.String(defaultEmailAddress),
			Destination: &ses.Destination{
				ToAddresses: []*string{aws.String(otpReq.Identifier)},
			},
			Message: &ses.Message{
				Subject: &ses.Content{
					Data: aws.String("Your OTP"),
				},
				Body: &ses.Body{
					Text: &ses.Content{
						Data: aws.String(fmt.Sprintf("Your OTP is: %s", otp)),
					},
				},
			},
		})
	default:
		return createResponse(http.StatusBadRequest, "Invalid method"), fmt.Errorf("invalid OTP send method: %s", otpReq.Method)
	}

	if err != nil {
		return createResponse(http.StatusInternalServerError, "Failed to send OTP"), fmt.Errorf("failed to send OTP: %w", err)
	}

	// Return the new auth key
	response := struct {
		Message string `json:"message"`
	}{
		Message: "OTP sent successfully",
	}

	jsonResponse, err := json.Marshal(response)
	if err != nil {
		return createResponse(http.StatusInternalServerError, "Failed to create response"), fmt.Errorf("failed to marshal response: %w", err)
	}

	return createResponse(http.StatusOK, string(jsonResponse)), nil

	//return createResponse(http.StatusOK, "OTP sent successfully"), nil
}

func main() {
	lambda.Start(handleRequest)
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	//fmt.Printf("Full request: %+v", request)

	// Remove trailing slash from path if present
	path := strings.TrimSuffix(request.Path, "/")

	switch {
	case request.HTTPMethod == "POST" && path == "/send-otp":
		return sendOTP(request)
	default:
		return createResponse(http.StatusNotFound, "Not Found"), fmt.Errorf("unknown endpoint: %s %s", request.HTTPMethod, request.Path)
	}
}
