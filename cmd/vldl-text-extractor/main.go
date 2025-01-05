package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/textract"
)

func handleS3Event(ctx context.Context, s3Event events.S3Event) error {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}

	textractClient := textract.NewFromConfig(cfg)
	opensearchClient := opensearch.NewFromConfig(cfg)
	fmt.Printf("textractClient: %v\n", textractClient)
	fmt.Printf("opensearchClient: %v\n", opensearchClient)

	/* 	for _, record := range s3Event.Records {
		// Extract text using Textract
		input := &textract.DetectDocumentTextInput{
			Document: &textract.Document{
				S3Object: &textract.S3Object{
					Bucket: aws.String(record.S3.Bucket.Name),
					Name:   aws.String(record.S3.Object.Key),
				},
			},
		}

		result, err := textractClient.DetectDocumentText(ctx, input)
		if err != nil {
			return err
		}

		// Index in OpenSearch
		for _, block := range result.Blocks {
			if block.BlockType == textract.BlockTypeWord {
				document := map[string]interface{}{
					"imageId":     record.S3.Object.Key,
					"text":        *block.Text,
					"confidence":  *block.Confidence,
					"boundingBox": block.Geometry.BoundingBox,
				}

				_, err = opensearchClient.Index(ctx, &opensearch.IndexRequest{
					Index:    "image-text",
					Document: document,
				})
				if err != nil {
					return err
				}
			}
		}
	} */
	return nil
}

func main() {
	lambda.Start(handleS3Event)
}
