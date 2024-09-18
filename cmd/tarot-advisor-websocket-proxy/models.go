package main

import (
	"encoding/json"
)

type Request struct {
	Type       string          `json:"type" validate:"required"`
	Parameters json.RawMessage `json:"parameters" validate:"required"`
}

type TripAdvisorRequest struct {
	NumAdults    int    `json:"num_adults" validate:"required,min=1"`
	NumKids      int    `json:"num_kids" validate:"gte=0"`
	KidsAges     []int  `json:"kids_ages" validate:"kidsAgesRequiredIfNumKids"`
	StartDate    string `json:"start_date" validate:"required"`
	EndDate      string `json:"end_date" validate:"required"`
	HotelName    string `json:"hotel_name" validate:"required,max=75"`
	HotelType    string `json:"hotel_type" validate:"required,max=20"`
	HotelRating  string `json:"hotel_rating" validate:"required,max=35"`
	HotelReviews int    `json:"hotel_reviews" validate:"required"`
	HotelRanking string `json:"hotel_ranking" validate:"required,max=75"`
	Cards        string `json:"cards" validate:"required,max=75"`
}

type IndeedRequest struct {
	JobTitle       string   `json:"job_title" validate:"required"`
	JobDescription string   `json:"job_description" validate:"required"`
	Skills         []string `json:"skills" validate:"required,dive,required"`
	Experience     int      `json:"experience" validate:"required,gte=0"`
	Education      string   `json:"education" validate:"required"`
	Location       string   `json:"location" validate:"required"`
	Salary         float64  `json:"salary" validate:"required,gte=0"`
	JobType        string   `json:"job_type" validate:"required"`
	ResumeText     string   `json:"resume_text" validate:"required"`
	Cards          string   `json:"cards" validate:"required"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AnthropicRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	System      string    `json:"system,omitempty"`
}

type Config struct {
	AnthropicURL     string
	AnthropicKey     string
	AnthropicModel   string
	AnthropicVersion string
}

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
