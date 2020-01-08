package config

// A single config parameter's validation result
type ParameterValidationState struct {
	// Whether the value is set
	Present bool

	// Error occurred while type casting (if any)
	TypeError error

	// Error occurred while validating (if any)
	ValidationError error

	// The value of the desired type (or nil)
	Value interface{}
}
