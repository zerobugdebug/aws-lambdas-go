package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	defaultAnthropicModel   = "claude-3-5-sonnet-20240620"
	defaultAnthropicVersion = "2023-06-01"
	connectRouteKey         = "$connect"
	disconnectRouteKey      = "$disconnect"
	envAnthropicURL         = "ANTHROPIC_URL"
	envAnthropicKey         = "ANTHROPIC_KEY"
	envAnthropicModel       = "ANTHROPIC_MODEL"
	envAnthropicVersion     = "ANTHROPIC_VERSION"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	PromptTemplate string    `json:"prompt_template"`
	Messages       []Message `json:"messages"`
}

type AnthropicResponse struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// AnthropicMessage represents a single message in the conversation
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AnthropicRequest represents the full request structure for the Anthropic API
type AnthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []AnthropicMessage `json:"messages"`
	Stream      bool               `json:"stream,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
	System      string             `json:"system,omitempty"`
}

type Config struct {
	AnthropicURL     string
	AnthropicKey     string
	AnthropicModel   string
	AnthropicVersion string
}

type Handler struct {
	dynamoClient *dynamodb.Client
	config       Config
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	awsCfg, err := awsConfig.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(fmt.Sprintf("Failed to load AWS config: %v", err))
	}

	dynamoClient := dynamodb.NewFromConfig(awsCfg)

	handler := &Handler{
		dynamoClient: dynamoClient,
		config:       cfg,
	}

	lambda.Start(handler.handleRequest)
}

func (h *Handler) handleRequest(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	switch event.RequestContext.RouteKey {
	case connectRouteKey:
		return h.handleConnect(ctx, event)
	case disconnectRouteKey:
		return h.handleDisconnect(ctx, event)
	default:
		return h.handleSendMessage(ctx, event)
	}
}

func (h *Handler) handleConnect(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	fmt.Printf("Client connected: %s\n", event.RequestContext.ConnectionID)
	authKey := event.Headers["Sec-WebSocket-Protocol"]

	userHash, err := h.getUserHashFromAuth(ctx, authKey)
	if err != nil {
		fmt.Printf("Failed to get user hash: %v\n", err)
		return createResponse(fmt.Sprintf("Failed to authenticate user: %v", err), http.StatusUnauthorized, nil)
	}

	err = h.storeConnectionInDynamoDB(ctx, event.RequestContext.ConnectionID, userHash)
	if err != nil {
		fmt.Printf("Failed to store connection: %v\n", err)
		return createResponse(fmt.Sprintf("Failed to store connection: %v", err), http.StatusInternalServerError, nil)
	}

	return createResponse("Connected successfully", http.StatusOK, map[string]string{"Sec-WebSocket-Protocol": authKey})

}

func (h *Handler) handleDisconnect(_ context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	fmt.Printf("Client disconnected: %s\n", event.RequestContext.ConnectionID)
	return createResponse("Disconnected successfully", http.StatusOK, map[string]string{"Sec-WebSocket-Protocol": event.Headers["Sec-WebSocket-Protocol"]})
}

func (h *Handler) handleSendMessage(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	fmt.Printf("event.Resource: %v\n", event.Resource)
	fmt.Printf("event.Path: %v\n", event.Path)
	fmt.Printf("event.HTTPMethod: %v\n", event.HTTPMethod)
	fmt.Printf("event.Body: %v\n", event.Body)
	fmt.Printf("event.RequestContext: %v\n", event.RequestContext)
	fmt.Printf("event.RequestContext.RouteKey: %v\n", event.RequestContext.RouteKey)

	// Parse the incoming request

	var req Request
	err := json.Unmarshal([]byte(event.Body), &req)
	if err != nil {
		return createResponse(fmt.Sprintf("Error parsing request JSON: %s", err), http.StatusBadRequest, nil)
	}

	// Create a channel to receive text blocks
	textChan := make(chan string)
	errorChan := make(chan error, 1)
	doneChan := make(chan struct{})

	go func() {
		defer close(textChan)
		err := h.callAnthropicAPI(req, textChan, doneChan)
		if err != nil {
			errorChan <- err
		}
		close(errorChan)
	}()

	wsClient, err := createWebSocketClient(ctx, event.RequestContext.DomainName, event.RequestContext.Stage)
	if err != nil {
		return createResponse(fmt.Sprintf("Failed to create WebSocket client: %v", err), http.StatusInternalServerError, nil)
	}
	fmt.Printf("wsClient: %v\n", wsClient)

	for {
		select {
		case text, ok := <-textChan:

			if !ok {
				return createResponse("Message processing completed", http.StatusOK, map[string]string{"Sec-WebSocket-Protocol": event.Headers["Sec-WebSocket-Protocol"]})
			}
			err = sendWebSocketMessage(ctx, wsClient, event.RequestContext.ConnectionID, text)
			if err != nil {
				return createResponse(fmt.Sprintf("Failed to send WebSocket message: %v", err), http.StatusInternalServerError, nil)
			}
		case err := <-errorChan:
			fmt.Printf("err: %v\n", err)
			if err != nil {
				return createResponse(fmt.Sprintf("Error calling Anthropic API: %v", err), http.StatusInternalServerError, nil)
			}
		case <-doneChan:
			// Close the WebSocket connection
			err = closeWebSocketConnection(ctx, wsClient, event.RequestContext.ConnectionID)
			if err != nil {
				return createResponse(fmt.Sprintf("Failed to close WebSocket connection: %v", err), http.StatusInternalServerError, nil)
			}
			userHash, err := h.getUserHashFromConnection(ctx, event.RequestContext.ConnectionID)
			if err != nil {
				fmt.Printf("Failed to get user hash: %v\n", err)
				return createResponse(fmt.Sprintf("Failed to authenticate user: %v", err), http.StatusUnauthorized, nil)
			}
			err = h.decreaseRemainingRequests(ctx, userHash)
			if err != nil {
				fmt.Printf("Failed to decrease remaining requests: %v\n", err)
			}
			err = h.removeConnectionFromDynamoDB(ctx, event.RequestContext.ConnectionID)
			if err != nil {
				fmt.Printf("Failed to remove connection from DB: %v\n", err)
			}
			return createResponse("Message processing completed", http.StatusOK, map[string]string{"Sec-WebSocket-Protocol": event.Headers["Sec-WebSocket-Protocol"]})
		case <-ctx.Done():
			return createResponse("Request timeout", http.StatusGatewayTimeout, nil)
		}
	}
}

// createResponse creates an API Gateway response with a specified message and status code
func createResponse(message string, statusCode int, headers map[string]string) (events.APIGatewayProxyResponse, error) {

	response := events.APIGatewayProxyResponse{
		Body:       message,
		StatusCode: statusCode,
	}

	if len(headers) > 0 {
		response.Headers = headers
	}

	if statusCode >= 400 {
		return response, fmt.Errorf("HTTP %d: %s", statusCode, message)
	}

	return response, nil
}

func loadConfig() (Config, error) {
	cfg := Config{
		AnthropicURL:     os.Getenv(envAnthropicURL),
		AnthropicKey:     os.Getenv(envAnthropicKey),
		AnthropicModel:   os.Getenv(envAnthropicModel),
		AnthropicVersion: os.Getenv(envAnthropicVersion),
	}

	if cfg.AnthropicKey == "" {
		return cfg, fmt.Errorf("anthropic API key not found in environment variable %s", envAnthropicKey)
	}

	if cfg.AnthropicModel == "" {
		cfg.AnthropicModel = defaultAnthropicModel
	}

	if cfg.AnthropicVersion == "" {
		cfg.AnthropicVersion = defaultAnthropicVersion
	}

	if cfg.AnthropicURL == "" {
		return cfg, fmt.Errorf("anthropic API URL not found in environment variable %s", envAnthropicURL)
	}

	return cfg, nil
}

func (h *Handler) callAnthropicAPI(req Request, textChan chan<- string, doneChan chan<- struct{}) error {

	systemPrompt := os.Getenv(req.PromptTemplate)
	if systemPrompt == "" {
		fmt.Printf("System prompt [%s] was not found\n", req.PromptTemplate)
	}

	anthropicReq := convertToAnthropicRequest(req, h.config.AnthropicModel, systemPrompt)

	requestBody, err := marshalRequest(anthropicReq)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", h.config.AnthropicURL, bytes.NewReader(requestBody))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", h.config.AnthropicKey)
	httpReq.Header.Set("anthropic-version", h.config.AnthropicVersion)

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")

		} else if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			var eventData map[string]interface{}
			err := json.Unmarshal([]byte(data), &eventData)
			if err != nil {
				return err
			}

			switch currentEvent {
			case "message_start":
				fmt.Println("Message started")
			case "content_block_start":
				fmt.Println("Content block started")
			case "ping":
				fmt.Println("Received ping")
			case "content_block_delta":
				if delta, ok := eventData["delta"].(map[string]interface{}); ok {
					if textDelta, ok := delta["text"].(string); ok {
						textChan <- textDelta

					}
				}
			case "content_block_stop":
				fmt.Println("Content block stopped")
			case "message_delta":
				fmt.Println("Received message delta")
			case "message_stop":
				fmt.Println("Message stopped")
				close(doneChan)
				return nil
			default:
				fmt.Printf("Unhandled event type: %s\n", currentEvent)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func createWebSocketClient(ctx context.Context, domainName, stage string) (*apigatewaymanagementapi.Client, error) {
	cfg, err := awsConfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %v", err)
	}

	client := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {

		o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s/%s", domainName, stage))
	})

	return client, nil
}

func closeWebSocketConnection(ctx context.Context, client *apigatewaymanagementapi.Client, connectionID string) error {
	_, err := client.DeleteConnection(ctx, &apigatewaymanagementapi.DeleteConnectionInput{
		ConnectionId: aws.String(connectionID),
	})
	return err
}

func sendWebSocketMessage(ctx context.Context, client *apigatewaymanagementapi.Client, connectionID string, message string) error {
	_, err := client.PostToConnection(ctx, &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: aws.String(connectionID),
		Data:         []byte(message),
	})
	if err != nil {
		fmt.Printf("Failed to send WebSocket message: %v\n", err)
	}
	return err
}

func (h *Handler) storeConnectionInDynamoDB(ctx context.Context, connectionID, userHash string) error {

	item, err := attributevalue.MarshalMap(map[string]string{
		"connection_id": connectionID,
		"user_hash":     userHash,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal WS_CONNECTIONS item: %v", err)
	}

	input := &dynamodb.PutItemInput{
		TableName: aws.String("WS_CONNECTIONS"),
		Item:      item,
	}

	_, err = h.dynamoClient.PutItem(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to store connection in DynamoDB: %v", err)
	}

	return nil
}

func (h *Handler) getUserHashFromAuth(ctx context.Context, authKey string) (string, error) {

	input := &dynamodb.GetItemInput{
		TableName: aws.String("AUTH"),
		Key: map[string]types.AttributeValue{
			"key": &types.AttributeValueMemberS{Value: authKey},
		},
	}

	result, err := h.dynamoClient.GetItem(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to get item from AUTH table: %v", err)
	}

	if result.Item == nil {
		return "", fmt.Errorf("no item found for auth key: %s", authKey)
	}

	var authItem struct {
		UserHash string `dynamodbav:"user_hash"`
	}

	err = attributevalue.UnmarshalMap(result.Item, &authItem)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal AUTH item: %v", err)
	}

	return authItem.UserHash, nil
}

func (h *Handler) getUserHashFromConnection(ctx context.Context, connectionID string) (string, error) {

	input := &dynamodb.GetItemInput{
		TableName: aws.String("WS_CONNECTIONS"),
		Key: map[string]types.AttributeValue{
			"connection_id": &types.AttributeValueMemberS{Value: connectionID},
		},
	}

	result, err := h.dynamoClient.GetItem(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to get item from WS_CONNECTIONS table: %v", err)
	}

	if result.Item == nil {
		return "", fmt.Errorf("no item found for connection ID: %s", connectionID)
	}

	var connectionItem struct {
		UserHash string `dynamodbav:"user_hash"`
	}

	err = attributevalue.UnmarshalMap(result.Item, &connectionItem)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal WS_CONNECTIONS item: %v", err)
	}

	return connectionItem.UserHash, nil
}

func (h *Handler) removeConnectionFromDynamoDB(ctx context.Context, connectionID string) error {

	input := &dynamodb.DeleteItemInput{
		TableName: aws.String("WS_CONNECTIONS"),
		Key: map[string]types.AttributeValue{
			"connection_id": &types.AttributeValueMemberS{Value: connectionID},
		},
	}

	_, err := h.dynamoClient.DeleteItem(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to remove connection from DynamoDB: %v", err)
	}

	return nil
}

func (h *Handler) decreaseRemainingRequests(ctx context.Context, userHash string) error {

	updateExpression := "SET remaining_requests = remaining_requests - :decr"
	expressionAttributeValues := map[string]types.AttributeValue{
		":decr": &types.AttributeValueMemberN{Value: "1"},
	}

	input := &dynamodb.UpdateItemInput{
		TableName: aws.String("USERS"),
		Key: map[string]types.AttributeValue{
			"user_hash": &types.AttributeValueMemberS{Value: userHash},
		},
		UpdateExpression:          aws.String(updateExpression),
		ExpressionAttributeValues: expressionAttributeValues,
	}

	_, err := h.dynamoClient.UpdateItem(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to update DynamoDB item: %v", err)
	}

	return nil
}

// NewAnthropicRequest creates a new AnthropicRequest with default values
func newAnthropicRequest(model, system string, messages []AnthropicMessage) *AnthropicRequest {
	return &AnthropicRequest{
		Model:     model,
		MaxTokens: 1024,
		Messages:  messages,
		Stream:    true,
		System:    system,
	}
}

func marshalRequest(req *AnthropicRequest) ([]byte, error) {
	return json.Marshal(req)
}

func convertToAnthropicRequest(req Request, model, system string) *AnthropicRequest {
	messages := make([]AnthropicMessage, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = AnthropicMessage(msg)
	}
	return newAnthropicRequest(model, system, messages)
}
