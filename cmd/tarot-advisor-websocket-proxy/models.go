package main

import (
	"encoding/json"
)

type Request struct {
	Type       string          `json:"type" validate:"required"`
	Parameters json.RawMessage `json:"parameters" validate:"required"`
}

type TripAdvisorRequest struct {
	NumAdults    int     `json:"num_adults" validate:"required,min=1"`
	NumKids      int     `json:"num_kids" validate:"gte=0"`
	KidsAges     []int   `json:"kids_ages" validate:"dive,min=0"`
	StartDate    string  `json:"start_date" validate:"required,datetime=2006-01-02"`
	EndDate      string  `json:"end_date" validate:"required,datetime=2006-01-02"`
	HotelName    string  `json:"hotel_name" validate:"required"`
	HotelType    string  `json:"hotel_type" validate:"required"`
	HotelRating  float64 `json:"hotel_rating" validate:"required,gte=0,lte=5"`
	HotelReviews int     `json:"hotel_reviews" validate:"required"`
	HotelRanking int     `json:"hotel_ranking" validate:"required"`
	Cards        string  `json:"cards" validate:"required"`
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
