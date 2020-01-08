package config

import "strconv"

// Parameter provides the constraints for a single config parameter.
type Parameter struct {
	// PreCondition takes information about known parameters, returns whether this parameter may appear.
	PreCondition func(map[string]map[string]*ParameterValidationState) bool

	// Required takes information about known parameters, returns whether this parameter must appear.
	Required func(map[string]map[string]*ParameterValidationState) bool

	// Default is the default value of the desired type (if parameter missing).
	Default interface{}

	// TypeParser takes the raw value and converts it to the desired type.
	TypeParser func(string) (interface{}, error)

	// Validator takes the converted value and validates it.
	Validator func(interface{}) error
}

// NoPreCondition is a Parameter#PreCondition without any actual precondition.
func NoPreCondition(map[string]map[string]*ParameterValidationState) bool {
	return true
}

// Required is a Parameter#Required requiring the parameter unconditionally.
func Required(map[string]map[string]*ParameterValidationState) bool {
	return true
}

// Optional is a Parameter#Required not requiring the parameter unconditionally.
func Optional(map[string]map[string]*ParameterValidationState) bool {
	return false
}

// TypeString is a Parameter#TypeParser not requiring the parameter to be of any special type.
func TypeString(s string) (interface{}, error) {
	return s, nil
}

// TypeUInt64 is a Parameter#TypeParser requiring the parameter to be an integer.
func TypeUInt64(s string) (interface{}, error) {
	return strconv.ParseUint(s, 10, 64)
}

// NoValidator is a Parameter#Validator without any actual validator.
func NoValidator(interface{}) error {
	return nil
}
