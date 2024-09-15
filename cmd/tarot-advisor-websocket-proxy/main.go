package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/go-playground/validator/v10"

)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(fmt.Sprintf("Failed to load AWS config: %v", err))
	}

	dynamoClient := NewDynamoClient(awsCfg)
	validate := validator.New()

	handler := NewHandler(cfg, dynamoClient, validate)
	lambda.Start(handler.HandleRequest)
}
