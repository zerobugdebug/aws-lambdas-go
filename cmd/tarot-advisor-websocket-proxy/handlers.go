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
	"text/template"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/go-playground/validator/v10"
)

type Handler struct {
	dynamoClient *DynamoClient
	config       Config
	validator    *validator.Validate
}

func NewHandler(cfg Config, dynamoClient *DynamoClient, v *validator.Validate) *Handler {
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

func (h *Handler) handleSendMessage(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	var req Request
	err := json.Unmarshal([]byte(event.Body), &req)
	if err != nil {
		return createResponse(fmt.Sprintf("Error parsing request JSON: %s", err), http.StatusBadRequest, nil)
	}

	// Validate the request type
	err = h.validator.Struct(req)
	if err != nil {
		return createResponse(fmt.Sprintf("Validation error: %s", err), http.StatusBadRequest, nil)
	}

	var content, systemPrompt string

	switch req.Type {
	case "tripadvisor_request":
		var taReq TripAdvisorRequest
		err := json.Unmarshal(req.Parameters, &taReq)
		if err != nil {
			return createResponse(fmt.Sprintf("Error parsing parameters: %s", err), http.StatusBadRequest, nil)
		}

		// Validate taReq
		err = h.validator.Struct(taReq)
		if err != nil {
			return createResponse(fmt.Sprintf("Validation error: %s", err), http.StatusBadRequest, nil)
		}

		// Process templates from environment variables
		content, err = h.processTemplateFromEnv("TRIPADVISOR_TEMPLATE", taReq)
		if err != nil {
			return createResponse(fmt.Sprintf("Error processing template: %s", err), http.StatusInternalServerError, nil)
		}

		systemPrompt, err = h.processTemplateFromEnv("TAROTREADING_SYSTEM_PROMPT", taReq)
		if err != nil {
			return createResponse(fmt.Sprintf("Error processing system prompt template: %s", err), http.StatusInternalServerError, nil)
		}

	case "indeed_request":
		var indeedReq IndeedRequest
		err := json.Unmarshal(req.Parameters, &indeedReq)
		if err != nil {
			return createResponse(fmt.Sprintf("Error parsing parameters: %s", err), http.StatusBadRequest, nil)
		}

		// Validate indeedReq
		err = h.validator.Struct(indeedReq)
		if err != nil {
			return createResponse(fmt.Sprintf("Validation error: %s", err), http.StatusBadRequest, nil)
		}

		// Process templates from environment variables
		content, err = h.processTemplateFromEnv("INDEED_TEMPLATE", indeedReq)
		if err != nil {
			return createResponse(fmt.Sprintf("Error processing template: %s", err), http.StatusInternalServerError, nil)
		}

		systemPrompt, err = h.processTemplateFromEnv("INDEED_SYSTEM_PROMPT", indeedReq)
		if err != nil {
			return createResponse(fmt.Sprintf("Error processing system prompt template: %s", err), http.StatusInternalServerError, nil)
		}

	default:
		return createResponse(fmt.Sprintf("Unknown request type: %s", req.Type), http.StatusBadRequest, nil)
	}

	// Build the Anthropic request
	anthropicReq := h.buildAnthropicRequest(content, systemPrompt)

	// Call Anthropic API and handle response
	textChan := make(chan string)
	errorChan := make(chan error, 1)
	doneChan := make(chan struct{})

	go func() {
		defer close(textChan)
		err := h.callAnthropicAPI(anthropicReq, textChan, doneChan)
		if err != nil {
			errorChan <- err
		}
		close(errorChan)
	}()

	// Create WebSocket client
	wsClient, err := createWebSocketClient(ctx, event.RequestContext.DomainName, event.RequestContext.Stage)
	if err != nil {
		return createResponse(fmt.Sprintf("Failed to create WebSocket client: %v", err), http.StatusInternalServerError, nil)
	}

	// Send responses over WebSocket
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
			if err != nil {
				return createResponse(fmt.Sprintf("Error calling Anthropic API: %v", err), http.StatusInternalServerError, nil)
			}
		case <-doneChan:
			// Close the WebSocket connection and clean up
			err = closeWebSocketConnection(ctx, wsClient, event.RequestContext.ConnectionID)
			if err != nil {
				return createResponse(fmt.Sprintf("Failed to close WebSocket connection: %v", err), http.StatusInternalServerError, nil)
			}
			// Additional cleanup if necessary
			return createResponse("Message processing completed", http.StatusOK, map[string]string{"Sec-WebSocket-Protocol": event.Headers["Sec-WebSocket-Protocol"]})
		case <-ctx.Done():
			return createResponse("Request timeout", http.StatusGatewayTimeout, nil)
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

func (h *Handler) callAnthropicAPI(req *AnthropicRequest, textChan chan<- string, doneChan chan<- struct{}) error {
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

func formatWithCommas(n int) string {
	in := strconv.Itoa(n)
	nLen := len(in)
	if nLen <= 3 {
		return in
	}
	out := ""
	for i, v := range in {
		if i != 0 && (nLen-i)%3 == 0 {
			out += ","
		}
		out += string(v)
	}
	return out
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
