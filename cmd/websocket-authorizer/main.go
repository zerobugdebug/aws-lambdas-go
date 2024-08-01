package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	defaultTableName = "AUTH"
)

// Help function to generate an IAM policy
func generatePolicy(principalId, effect, resource string) events.APIGatewayCustomAuthorizerResponse {
	authResponse := events.APIGatewayCustomAuthorizerResponse{PrincipalID: principalId}
	fmt.Printf("authResponse: %v+\n", authResponse)
	fmt.Printf("effect: %v\n", effect)
	fmt.Printf("resource: %v\n", resource)

	if effect != "" && resource != "" {
		authResponse.PolicyDocument = events.APIGatewayCustomAuthorizerPolicy{
			Version: "2012-10-17",
			Statement: []events.IAMPolicyStatement{
				{
					Action:   []string{"execute-api:Invoke"},
					Effect:   effect,
					Resource: []string{resource},
				},
			},
		}
	}

	// Optional output with custom properties of the String, Number or Boolean type.
	authResponse.Context = map[string]interface{}{
		"stringKey":  "stringval",
		"numberKey":  123,
		"booleanKey": true,
	}
	return authResponse
}

func handleRequest(ctx context.Context, event events.APIGatewayV2CustomAuthorizerV1Request) (events.APIGatewayCustomAuthorizerResponse, error) {
	fmt.Printf("event: %+v\n", event)

	// Extract the auth key from Sec-WebSocket-Protocol header
	authKey, ok := event.Headers["Sec-WebSocket-Protocol"]
	if !ok {
		return events.APIGatewayCustomAuthorizerResponse{}, errors.New("missing Sec-WebSocket-Protocol header")
		//return events.APIGatewayCustomAuthorizerResponse{}, errors.New("missing Sec-WebSocket-Protocol header")
	}

	fmt.Printf("authKey before split: %v\n", authKey)
	// If multiple protocols are specified, use the first one as the auth key
	authKey = strings.Split(authKey, ",")[0]
	authKey = strings.TrimSpace(authKey)
	fmt.Printf("authKey: %v\n", authKey)
	// Initialize DynamoDB client
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Printf("Can't connect to DynamoDB: %s\n", err)
		return events.APIGatewayCustomAuthorizerResponse{}, err
	}

	client := dynamodb.NewFromConfig(cfg)

	// Check if the auth key exists in DynamoDB
	tableName := os.Getenv("AUTH_TABLE_NAME")
	if tableName == "" {
		tableName = defaultTableName
	}
	fmt.Printf("tableName: %v\n", tableName)

	result, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(tableName),
		Key: map[string]types.AttributeValue{
			"key": &types.AttributeValueMemberS{Value: authKey},
		},
	})

	if err != nil {
		fmt.Printf("Can't query DynamoDB: %s\n", err)
		return events.APIGatewayCustomAuthorizerResponse{}, err
	}

	if result.Item == nil {
		fmt.Printf("Can't find auth key: %s\n", authKey)
		return generatePolicy("user", "Deny", event.MethodArn), nil
	}

	// If auth key is valid, return an "Allow" policy
	//return events.APIGatewayV2CustomAuthorizerSimpleResponse{IsAuthorized: true}, nil
	// If auth key is valid, return an "Allow" policy
	return generatePolicy("user", "Allow", event.MethodArn), nil
}

func main() {
	lambda.Start(handleRequest)
}
