package main

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

)

type DynamoClient struct {
	client *dynamodb.Client
}

func NewDynamoClient(cfg aws.Config) *DynamoClient {
	return &DynamoClient{
		client: dynamodb.NewFromConfig(cfg),
	}
}

func (dc *DynamoClient) GetItem(ctx context.Context, input *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
	return dc.client.GetItem(ctx, input)
}

func (dc *DynamoClient) PutItem(ctx context.Context, input *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
	return dc.client.PutItem(ctx, input)
}

func (dc *DynamoClient) DeleteItem(ctx context.Context, input *dynamodb.DeleteItemInput) (*dynamodb.DeleteItemOutput, error) {
	return dc.client.DeleteItem(ctx, input)
}

func (dc *DynamoClient) UpdateItem(ctx context.Context, input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
	return dc.client.UpdateItem(ctx, input)
}
