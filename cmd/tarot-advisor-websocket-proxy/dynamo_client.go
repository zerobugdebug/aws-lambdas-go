package main

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type DynamoClient interface {
	GetItem(ctx context.Context, input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, input *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error)
	DeleteItem(ctx context.Context, input *dynamodb.DeleteItemInput) (*dynamodb.DeleteItemOutput, error)
	UpdateItem(ctx context.Context, input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error)
}

type dynamoClient struct {
	client *dynamodb.Client
}

func NewDynamoClient(cfg aws.Config) DynamoClient {
	return &dynamoClient{
		client: dynamodb.NewFromConfig(cfg),
	}
}

func (dc *dynamoClient) GetItem(ctx context.Context, input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
	return dc.client.GetItem(ctx, input)
}

func (dc *dynamoClient) PutItem(ctx context.Context, input *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
	return dc.client.PutItem(ctx, input)
}

func (dc *dynamoClient) DeleteItem(ctx context.Context, input *dynamodb.DeleteItemInput) (*dynamodb.DeleteItemOutput, error) {
	return dc.client.DeleteItem(ctx, input)
}

func (dc *dynamoClient) UpdateItem(ctx context.Context, input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
	return dc.client.UpdateItem(ctx, input)
}
