package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/go-playground/validator/v10"
)

type Handler struct {
	dynamoClient DynamoClient
	config       Config
	validator    *validator.Validate
}

func NewHandler(cfg Config, dynamoClient DynamoClient, v *validator.Validate) *Handler {
	RegisterCustomValidators(v)
	return &Handler{
		dynamoClient: dynamoClient,
		config:       cfg,
		validator:    v,
	}
}

func (h *Handler) HandleRequest(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
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

	if authKey == "" {
		fmt.Println("No auth key provided in Sec-WebSocket-Protocol header")
		return h.closeConnection(ctx, event, "Authentication required")
	}

	userHash, err := h.getUserHashFromAuth(ctx, authKey)
	if err != nil {
		fmt.Printf("Failed to get user hash: %v\n", err)
		return h.closeConnection(ctx, event, "Failed to authenticate user")
	}

	err = h.storeConnectionInDynamoDB(ctx, event.RequestContext.ConnectionID, userHash)
	if err != nil {
		fmt.Printf("Failed to store connection: %v\n", err)
		return h.closeConnection(ctx, event, "Failed to store connection")
	}

	return createResponse("", http.StatusOK, map[string]string{"Sec-WebSocket-Protocol": authKey})
}

func (h *Handler) handleDisconnect(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	fmt.Printf("Client disconnected: %s\n", event.RequestContext.ConnectionID)
	err := h.removeConnectionFromDynamoDB(ctx, event.RequestContext.ConnectionID)
	if err != nil {
		fmt.Printf("Failed to remove connection from DB: %v\n", err)

	}
	return createResponse("", http.StatusOK, nil)
}

func (h *Handler) handleSendMessage(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Get the user hash from the connection
	userHash, err := h.getUserHashFromConnection(ctx, event.RequestContext.ConnectionID)
	if err != nil {
		return h.closeConnection(ctx, event, fmt.Sprintf("Failed to retrieve user: %v", err))
	}

	// Check remaining requests
	remainingRequests, err := h.getRemainingRequests(ctx, userHash)
	if err != nil {
		return h.closeConnection(ctx, event, fmt.Sprintf("Failed to check remaining tokens: %v", err))
	}

	// If remaining_requests <= 0, deny request
	if remainingRequests <= 0 {
		return h.closeConnection(ctx, event, "You have no remaining tokens available")
	}

	var req Request
	err = json.Unmarshal([]byte(event.Body), &req)
	if err != nil {
		return h.closeConnection(ctx, event, fmt.Sprintf("Error parsing request JSON: %s", err))
	}

	// Validate the request type
	err = h.validator.Struct(req)
	if err != nil {
		return h.closeConnection(ctx, event, fmt.Sprintf("Validation error: %s", err))
	}

	var content, systemPrompt string

	switch req.Type {
	case "tripadvisor_request":
		var taReq TripAdvisorRequest
		err := json.Unmarshal(req.Parameters, &taReq)
		if err != nil {
			return h.closeConnection(ctx, event, fmt.Sprintf("Error parsing parameters: %s", err))
		}
		fmt.Printf("handleSendMessage taReq: %v\n", taReq)

		// Validate taReq
		err = h.validator.Struct(taReq)
		if err != nil {
			return h.closeConnection(ctx, event, fmt.Sprintf("Validation error: %s", err))
		}

		// Process templates from environment variables
		content, err = h.processTemplateFromEnv("TRIPADVISOR_TEMPLATE", taReq)
		if err != nil {
			return h.closeConnection(ctx, event, fmt.Sprintf("Error processing template: %s", err))
		}

		systemPrompt, err = h.processTemplateFromEnv("TAROTREADING_SYSTEM_PROMPT", taReq)
		if err != nil {
			return h.closeConnection(ctx, event, fmt.Sprintf("Error processing system prompt template: %s", err))
		}

	case "indeed_request":
		var indeedReq IndeedRequest
		err := json.Unmarshal(req.Parameters, &indeedReq)
		if err != nil {
			return h.closeConnection(ctx, event, fmt.Sprintf("Error parsing parameters: %s", err))
		}

		// Validate indeedReq
		err = h.validator.Struct(indeedReq)
		if err != nil {
			return h.closeConnection(ctx, event, fmt.Sprintf("Validation error: %s", err))
		}

		// Process templates from environment variables
		content, err = h.processTemplateFromEnv("INDEED_TEMPLATE", indeedReq)
		if err != nil {
			return h.closeConnection(ctx, event, fmt.Sprintf("Error processing template: %s", err))
		}

		systemPrompt, err = h.processTemplateFromEnv("INDEED_SYSTEM_PROMPT", indeedReq)
		if err != nil {
			return h.closeConnection(ctx, event, fmt.Sprintf("Error processing system prompt template: %s", err))
		}

	default:
		return h.closeConnection(ctx, event, fmt.Sprintf("Unknown request type: %s", req.Type))
	}

	// Build the Anthropic request
	anthropicReq := h.buildAnthropicRequest(content, systemPrompt)

	// Call Anthropic API and handle response
	textChan := make(chan string)
	errorChan := make(chan error, 1)
	doneChan := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		err := h.callAnthropicAPI(anthropicReq, textChan, doneChan, &wg)
		if err != nil {
			errorChan <- err
		}
		close(errorChan)
	}()

	// Create WebSocket client
	wsClient, err := createWebSocketClient(ctx, event.RequestContext.DomainName, event.RequestContext.Stage)
	if err != nil {
		return h.closeConnection(ctx, event, fmt.Sprintf("Failed to create WebSocket client: %v", err))
	}

	// Send responses over WebSocket
	for {
		select {
		case text, ok := <-textChan:
			if !ok {
				return createResponse("", http.StatusOK, nil)
			}
			err = sendWebSocketMessage(ctx, wsClient, event.RequestContext.ConnectionID, text)
			if err != nil {
				return h.closeConnection(ctx, event, fmt.Sprintf("Failed to send WebSocket message: %v", err))
			}
		case err := <-errorChan:
			if err != nil {
				return h.closeConnection(ctx, event, fmt.Sprintf("Error calling Anthropic API: %v", err))
			}
		case <-doneChan:
			fmt.Println("Received doneChan")
			userHash, err := h.getUserHashFromConnection(ctx, event.RequestContext.ConnectionID)
			if err != nil {
				fmt.Printf("Failed to get user hash: %v\n", err)
			} else {

				err = h.decreaseRemainingRequests(ctx, userHash)
				if err != nil {
					fmt.Printf("Failed to decrease remaining requests: %v\n", err)
				}

			}
			fmt.Println("Closing connection")
			return h.closeConnection(ctx, event, "")
		case <-ctx.Done():
			return h.closeConnection(ctx, event, "Request timeout")
		}
	}
}

func (h *Handler) processTemplateFromEnv(envVar string, data interface{}) (string, error) {
	templateText := os.Getenv(envVar)
	if templateText == "" {
		return "", fmt.Errorf("environment variable %s not set", envVar)
	}

	funcMap := template.FuncMap{
		"joinInts": func(ints []int, sep string) string {
			strInts := make([]string, len(ints))
			for i, v := range ints {
				strInts[i] = strconv.Itoa(v)
			}
			return strings.Join(strInts, sep)
		},
		"commaFormat": func(n int) string {
			return formatWithCommas(n)
		},
		"joinStrings": func(strs []string, sep string) string {
			return strings.Join(strs, sep)
		},
	}

	tmpl, err := template.New(envVar).Funcs(funcMap).Parse(templateText)
	if err != nil {
		return "", fmt.Errorf("failed to parse template from %s: %v", envVar, err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %v", err)
	}

	return buf.String(), nil
}

func (h *Handler) buildAnthropicRequest(content, systemPrompt string) *AnthropicRequest {
	messages := []Message{
		{
			Role:    "user",
			Content: content,
		},
	}

	return &AnthropicRequest{
		Model:     h.config.AnthropicModel,
		MaxTokens: 1024,
		Messages:  messages,
		Stream:    true,
		System:    systemPrompt,
	}
}

func (h *Handler) callAnthropicAPI(req *AnthropicRequest, textChan chan<- string, doneChan chan<- struct{}, wg *sync.WaitGroup) error {
	defer wg.Done()

	requestBody, err := json.Marshal(req)
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
				fmt.Printf("Closing doneChan: %v\n", doneChan)
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

	close(doneChan)

	return nil
}

func (h *Handler) getUserHashFromAuth(ctx context.Context, authKey string) (string, error) {
	if authKey == "" {
		return "", fmt.Errorf("auth key is empty")
	}

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

func (h *Handler) getRemainingRequests(ctx context.Context, userHash string) (int, error) {
	input := &dynamodb.GetItemInput{
		TableName: aws.String("USERS"),
		Key: map[string]types.AttributeValue{
			"user_hash": &types.AttributeValueMemberS{Value: userHash},
		},
	}

	result, err := h.dynamoClient.GetItem(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("failed to get item from USERS table: %v", err)
	}

	if result.Item == nil {
		return 0, fmt.Errorf("no item found for user hash: %s", userHash)
	}

	var userItem struct {
		RemainingRequests int `dynamodbav:"remaining_requests"`
	}

	err = attributevalue.UnmarshalMap(result.Item, &userItem)
	if err != nil {
		return 0, fmt.Errorf("failed to unmarshal USERS item: %v", err)
	}

	return userItem.RemainingRequests, nil
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

func formatWithCommas(n int) string {
	return strconv.FormatInt(int64(n), 10)
}

func createWebSocketClient(ctx context.Context, domainName, stage string) (*apigatewaymanagementapi.Client, error) {
	cfg, err := awsConfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %v", err)
	}

	// endpoint := fmt.Sprintf("https://%s/%s", domainName, stage)
	// client := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
	// 	o.EndpointResolver = apigatewaymanagementapi.EndpointResolverFromURL(endpoint)

	client := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s/%s", domainName, stage))
	})

	return client, nil
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

func (h *Handler) closeConnection(ctx context.Context, event events.APIGatewayWebsocketProxyRequest, message string) (events.APIGatewayProxyResponse, error) {
	// Create WebSocket client
	wsClient, err := createWebSocketClient(ctx, event.RequestContext.DomainName, event.RequestContext.Stage)
	if err != nil {
		fmt.Printf("Failed to create WebSocket client: %v\n", err)
		return createResponse(message, http.StatusInternalServerError, nil)
	}

	// Send error message to client
	if message != "" {
		sendWebSocketMessage(ctx, wsClient, event.RequestContext.ConnectionID, message)
	}

	// Close WebSocket connection
	err = closeWebSocketConnection(ctx, wsClient, event.RequestContext.ConnectionID)
	if err != nil {
		fmt.Printf("Failed to close WebSocket connection: %v\n", err)
	}

	// Remove connection from DynamoDB
	err = h.removeConnectionFromDynamoDB(ctx, event.RequestContext.ConnectionID)
	if err != nil {
		fmt.Printf("Failed to remove connection from DB: %v\n", err)
	}

	return createResponse("", http.StatusOK, nil)
}

func closeWebSocketConnection(ctx context.Context, client *apigatewaymanagementapi.Client, connectionID string) error {
	_, err := client.DeleteConnection(ctx, &apigatewaymanagementapi.DeleteConnectionInput{
		ConnectionId: aws.String(connectionID),
	})
	return err
}

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
