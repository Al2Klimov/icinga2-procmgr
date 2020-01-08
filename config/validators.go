package config

import (
	"errors"
	"strings"
)

// OneOf creates a Parameter#Validator that requires values to be one of the given options.
func OneOf(options []string) func(interface{}) error {
	return func(x interface{}) error {
		for _, v := range options {
			if x.(string) == v {
				return nil
			}
		}

		return errors.New("must be one of: " + strings.Join(options, ","))
	}
}
