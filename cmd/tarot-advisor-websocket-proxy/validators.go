package main

import (
	"github.com/go-playground/validator/v10"

)

func RegisterCustomValidators(v *validator.Validate) {
	// Register the custom validation function
	v.RegisterValidation("kidsAgesRequiredIfNumKids", validateKidsAgesRequiredIfNumKids)
}

func validateKidsAgesRequiredIfNumKids(fl validator.FieldLevel) bool {
	request := fl.Parent().Interface().(TripAdvisorRequest)
	if request.NumKids > 0 {
		return len(request.KidsAges) == request.NumKids
	}
	return len(request.KidsAges) == 0
}
