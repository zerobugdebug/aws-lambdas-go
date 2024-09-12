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

	"github.com/zerobugdebug/aws-lambdas-go/pkg/cipher"
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
		fmt.Printf("failed to unmarshal request: %v", err)
		return createResponse(http.StatusBadRequest, "Invalid request body"), nil
	}
	fmt.Printf("otpReq: %+v\n", otpReq)

	key, err := cipher.GenerateIDHash(otpReq.Identifier, otpReq.Method)
	if err != nil {
		fmt.Printf("invalid identifier: %v", err)
		return createResponse(http.StatusUnprocessableEntity, "Invalid identifier"), nil
	}

	otp := generateOTP()
	fmt.Printf("Generated OTP: %v\n", otp)

	sess := session.Must(session.NewSession())

	// Store OTP in DynamoDB
	dynamoClient := dynamodb.New(sess)
	_, err = dynamoClient.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String("OTP"),
		Item: map[string]*dynamodb.AttributeValue{
			"Identifier": {S: aws.String(key)},
			"CreatedAt":  {N: aws.String(strconv.FormatInt(time.Now().Unix(), 10))},
			"OTP":        {S: aws.String(otp)},
			"Active":     {BOOL: aws.Bool(true)},
		},
	})
	if err != nil {
		fmt.Printf("failed to store OTP in DynamoDB: %v", err)
		return createResponse(http.StatusInternalServerError, "Failed to store OTP"), nil
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
		fmt.Printf("invalid OTP send method: %s", otpReq.Method)
		return createResponse(http.StatusBadRequest, "Invalid method"), nil
	}

	if err != nil {
		fmt.Printf("failed to send OTP: %v", err)
		return createResponse(http.StatusInternalServerError, "Failed to send OTP"), nil
	}

	// Return the new auth key
	response := struct {
		Message string `json:"message"`
	}{
		Message: "OTP sent successfully",
	}

	jsonResponse, err := json.Marshal(response)
	if err != nil {
		fmt.Printf("failed to marshal response: %v", err)
		return createResponse(http.StatusInternalServerError, "Failed to create response"), nil
	}

	return createResponse(http.StatusOK, string(jsonResponse)), nil

}

func main() {
	lambda.Start(handleRequest)
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Remove trailing slash from path if present
	path := strings.TrimSuffix(request.Path, "/")

	switch {
	case request.HTTPMethod == "POST" && path == "/send-otp":
		return sendOTP(request)
	default:
		fmt.Printf("unknown endpoint: %s %s", request.HTTPMethod, request.Path)
		return createResponse(http.StatusNotFound, "Not Found"), nil
	}
}
