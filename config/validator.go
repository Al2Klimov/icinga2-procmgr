package config

import "errors"

// Validator validates config parameters.
type Validator map[string]map[string]*Parameter

// Validate validates the given config parameters and returns the parameters casted to the specified types.
func (v *Validator) Validate(cfg map[string]map[string]string) (map[string]map[string]interface{}, error) {
	states := v.ensureTypes(cfg)
	v.ensureValid(states)
	known, required := v.getKnownRequired(states)

	for section, options := range cfg {
		optionKnown, sectionIsKnown := known[section]
		if !sectionIsKnown {
			return nil, errors.New("bad section: " + section)
		}

		for option := range options {
			optionNotIgnored, optionIsKnown := optionKnown[option]
			if !optionIsKnown || !optionNotIgnored {
				return nil, errors.New("bad option: " + section + "." + option)
			}
		}
	}

	for section, options := range required {
		optionStates := states[section]

		for option, isRequired := range options {
			if isRequired && !optionStates[option].Present {
				return nil, errors.New("missing option: " + section + "." + option)
			}
		}
	}

	for section, options := range states {
		for option, state := range options {
			if state.TypeError != nil {
				return nil, errors.New("bad type of option: " + section + "." + option + ": " + state.TypeError.Error())
			}
		}
	}

	for section, options := range states {
		for option, state := range options {
			if state.ValidationError != nil {
				return nil, errors.New("bad value of option: " + section + "." + option + ": " + state.ValidationError.Error())
			}
		}
	}

	result := map[string]map[string]interface{}{}

	for section, options := range states {
		resultSection := map[string]interface{}{}

		for option, state := range options {
			resultSection[option] = state.Value
		}

		result[section] = resultSection
	}

	return result, nil
}

// ensureTypes casts the given config parameters to the specified types.
func (v *Validator) ensureTypes(cfg map[string]map[string]string) map[string]map[string]*ParameterValidationState {
	states := map[string]map[string]*ParameterValidationState{}

	for section, options := range *v {
		optionStates := map[string]*ParameterValidationState{}

		for option, constraints := range options {
			state := &ParameterValidationState{
				Present: false,
				Value:   constraints.Default,
			}

			if sec, hasSection := cfg[section]; hasSection {
				if opt, hasOption := sec[option]; hasOption {
					state.Present = true
					state.Value = opt

					typed, errType := constraints.TypeParser(opt)
					state.TypeError = errType

					if errType == nil {
						state.Value = typed
					}
				}
			}

			optionStates[option] = state
		}

		states[section] = optionStates
	}

	return states
}

// ensureValid validates the given config parameters.
func (v *Validator) ensureValid(states map[string]map[string]*ParameterValidationState) {
	for section, options := range *v {
		optionStates := states[section]

		for option, constraints := range options {
			state := optionStates[option]

			if state.Present && state.TypeError == nil {
				state.ValidationError = constraints.Validator(state.Value)
			}
		}
	}
}

// getKnownRequired returns the sets of known and required config parameters.
func (v *Validator) getKnownRequired(states map[string]map[string]*ParameterValidationState) (known map[string]map[string]bool, required map[string]map[string]bool) {
	known = map[string]map[string]bool{}
	required = map[string]map[string]bool{}

	for section, options := range *v {
		optionKnown := map[string]bool{}
		optionRequired := map[string]bool{}

		for option, constraints := range options {
			if constraints.PreCondition(states) {
				optionKnown[option] = true
				optionRequired[option] = constraints.Required(states)
			} else {
				optionKnown[option] = false
				optionRequired[option] = false
			}
		}

		known[section] = optionKnown
		required[section] = optionRequired
	}

	return
}
