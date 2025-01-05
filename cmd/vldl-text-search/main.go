package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	aoss "github.com/aws/aws-sdk-go-v2/service/opensearchserverless"
)

type SearchRequest struct {
	Query string `json:"query"`
}

type SearchResult struct {
	ImageID     string  `json:"imageId"`
	Confidence  float64 `json:"confidence"`
	BoundingBox BBox    `json:"boundingBox"`
}

type BBox struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func handleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var searchReq SearchRequest
	if err := json.Unmarshal([]byte(request.Body), &searchReq); err != nil {
		return events.APIGatewayProxyResponse{
			StatusCode: 400,
			Body:       "Invalid request body",
		}, nil
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return events.APIGatewayProxyResponse{
			StatusCode: 500,
			Body:       "Failed to load AWS config",
		}, err
	}

	// Create OpenSearch Serverless client
	client := aoss.NewFromConfig(cfg)
	fmt.Printf("client: %v\n", client)

	// Create search query
	searchBody := map[string]interface{}{
		"query": map[string]interface{}{
			"match": map[string]interface{}{
				"text": searchReq.Query,
			},
		},
	}

	searchBodyJson, err := json.Marshal(searchBody)
	fmt.Printf("searchBodyJson: %v\n", searchBodyJson)
	if err != nil {
		return events.APIGatewayProxyResponse{
			StatusCode: 500,
			Body:       "Failed to create search query",
		}, err
	}

	// Perform search using OpenSearch Serverless
	/* 	searchInput := &aoss.SearchInput{
	   		CollectionName: aws.String("image-text"),
	   		Body:           strings.NewReader(string(searchBodyJson)),
	   	}

	   	searchOutput, err := client.Search(ctx, searchInput)
	   	if err != nil {
	   		return events.APIGatewayProxyResponse{
	   			StatusCode: 500,
	   			Body:       "Search failed",
	   		}, err
	   	} */

	// Parse search results
	/* 	var searchResponse map[string]interface{}
	   	if err := json.NewDecoder(searchOutput.Body).Decode(&searchResponse); err != nil {
	   		return events.APIGatewayProxyResponse{
	   			StatusCode: 500,
	   			Body:       "Failed to parse search results",
	   		}, err
	   	} */

	// Process and format results
	/* 	var results []SearchResult
	   	if hits, ok := searchResponse["hits"].(map[string]interface{}); ok {
	   		if hitsList, ok := hits["hits"].([]interface{}); ok {
	   			for _, hit := range hitsList {
	   				if hitMap, ok := hit.(map[string]interface{}); ok {
	   					if source, ok := hitMap["_source"].(map[string]interface{}); ok {
	   						result := SearchResult{
	   							ImageID:    source["imageId"].(string),
	   							Confidence: source["confidence"].(float64),
	   						}
	   						if bbox, ok := source["boundingBox"].(map[string]interface{}); ok {
	   							result.BoundingBox = BBox{
	   								Left:   bbox["Left"].(float64),
	   								Top:    bbox["Top"].(float64),
	   								Width:  bbox["Width"].(float64),
	   								Height: bbox["Height"].(float64),
	   							}
	   						}
	   						results = append(results, result)
	   					}
	   				}
	   			}
	   		}
	   	} */

	// Return response
	responseBody, _ := json.Marshal("")
	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Headers: map[string]string{
			"Content-Type":                "application/json",
			"Access-Control-Allow-Origin": "*",
		},
		Body: string(responseBody),
	}, nil
}

func main() {
	lambda.Start(handleRequest)
}
