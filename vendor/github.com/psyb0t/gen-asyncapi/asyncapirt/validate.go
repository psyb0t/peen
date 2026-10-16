package asyncapirt

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Validator is implemented by every generated struct, enum and union.
type Validator interface {
	Validate() error
}

// ValidateValue calls Validate when value implements Validator.
func ValidateValue(value any) error {
	validator, ok := value.(Validator)
	if !ok {
		return nil
	}

	if err := validator.Validate(); err != nil {
		return ctxerrors.Wrap(err, "validate value")
	}

	return nil
}

// Invalid returns a validation error for the named value.
func Invalid(name, reason string) error {
	return ctxerrors.Wrapf(commerr.ErrValidationFailed, "%s: %s", name, reason)
}

// CheckMinLength enforces minLength, counting characters rather than bytes.
func CheckMinLength(name, value string, limit int) error {
	if utf8.RuneCountInString(value) >= limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: shorter than %d characters",
		name,
		limit,
	)
}

// CheckMaxLength enforces maxLength, counting characters rather than bytes.
func CheckMaxLength(name, value string, limit int) error {
	if utf8.RuneCountInString(value) <= limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: longer than %d characters",
		name,
		limit,
	)
}

// patterns caches compiled patterns by source text.
//
//nolint:gochecknoglobals // process-wide regexp cache, safe for concurrent use
var patterns sync.Map

// CheckPattern enforces pattern. Patterns are compiled once and cached. A
// pattern Go cannot compile fails validation instead of panicking.
func CheckPattern(name, value, pattern string) error {
	compiled, err := compilePattern(pattern)
	if err != nil {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"%s: pattern %q: %v",
			name,
			pattern,
			err,
		)
	}

	if compiled.MatchString(value) {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: does not match %q",
		name,
		pattern,
	)
}

func compilePattern(pattern string) (*regexp.Regexp, error) {
	if cached, ok := patterns.Load(pattern); ok {
		compiled, _ := cached.(*regexp.Regexp)

		return compiled, nil
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "compile pattern")
	}

	patterns.Store(pattern, compiled)

	return compiled, nil
}

// CheckMinimum enforces minimum.
func CheckMinimum(name string, value, limit float64) error {
	if value >= limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %v is below the minimum %v",
		name,
		value,
		limit,
	)
}

// CheckMaximum enforces maximum.
func CheckMaximum(name string, value, limit float64) error {
	if value <= limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %v is above the maximum %v",
		name,
		value,
		limit,
	)
}

// CheckExclusiveMinimum enforces exclusiveMinimum.
func CheckExclusiveMinimum(name string, value, limit float64) error {
	if value > limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %v is not above %v",
		name,
		value,
		limit,
	)
}

// CheckExclusiveMaximum enforces exclusiveMaximum.
func CheckExclusiveMaximum(name string, value, limit float64) error {
	if value < limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %v is not below %v",
		name,
		value,
		limit,
	)
}

// CheckMinItems enforces minItems.
func CheckMinItems(name string, count, limit int) error {
	if count >= limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %d items, want at least %d",
		name,
		count,
		limit,
	)
}

// CheckMaxItems enforces maxItems.
func CheckMaxItems(name string, count, limit int) error {
	if count <= limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %d items, want at most %d",
		name,
		count,
		limit,
	)
}

// multipleOfTolerance absorbs float rounding, so 0.3 counts as a multiple of
// 0.1.
const multipleOfTolerance = 1e-9

// CheckMultipleOf enforces multipleOf.
func CheckMultipleOf(name string, value, divisor float64) error {
	if divisor <= 0 {
		return ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"%s: multipleOf %v is not positive",
			name,
			divisor,
		)
	}

	quotient := value / divisor
	if math.Abs(quotient-math.Round(quotient)) <= multipleOfTolerance {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %v is not a multiple of %v",
		name,
		value,
		divisor,
	)
}

// CheckUniqueItems enforces uniqueItems. Items compare by their JSON
// encoding, which is how JSON Schema defines equality.
func CheckUniqueItems[T any](name string, items []T) error {
	seen := make(map[string]int, len(items))

	for i, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"%s[%d]: %v",
				name,
				i,
				err,
			)
		}

		if first, duplicate := seen[string(encoded)]; duplicate {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"%s: items %d and %d are equal",
				name,
				first,
				i,
			)
		}

		seen[string(encoded)] = i
	}

	return nil
}

// CheckMinProperties enforces minProperties.
func CheckMinProperties(name string, count, limit int) error {
	if count >= limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %d properties, want at least %d",
		name,
		count,
		limit,
	)
}

// CheckMaxProperties enforces maxProperties.
func CheckMaxProperties(name string, count, limit int) error {
	if count <= limit {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: %d properties, want at most %d",
		name,
		count,
		limit,
	)
}

// RequireDependent enforces dependentRequired: when property is present, every
// name in required must be present too.
func RequireDependent(
	typeName string,
	members map[string]json.RawMessage,
	property string,
	required ...string,
) error {
	if _, present := members[property]; !present {
		return nil
	}

	for _, name := range required {
		if _, ok := members[name]; !ok {
			return ctxerrors.Wrapf(
				commerr.ErrRequiredFieldNotSet,
				"%s.%s, required when %s is set",
				typeName,
				name,
				property,
			)
		}
	}

	return nil
}

// CheckConst fails when got is not the value the schema pins with const.
func CheckConst[T comparable](name string, got, want T) error {
	if got == want {
		return nil
	}

	return ctxerrors.Wrapf(
		commerr.ErrValidationFailed,
		"%s: got %v, want %v",
		name,
		got,
		want,
	)
}

// DecodeObject splits a JSON object into its raw members, so generated
// UnmarshalJSON methods can check required and unknown keys.
func DecodeObject(
	data []byte,
	typeName string,
) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, DecodeError(typeName, err)
	}

	if members == nil {
		return nil, ctxerrors.Wrapf(
			commerr.ErrCouldNotDecode,
			"%s: null is not an object",
			typeName,
		)
	}

	return members, nil
}

// RequireKeys fails when a required member is missing.
func RequireKeys(
	typeName string,
	members map[string]json.RawMessage,
	names ...string,
) error {
	for _, name := range names {
		if _, ok := members[name]; !ok {
			return ctxerrors.Wrapf(
				commerr.ErrRequiredFieldNotSet,
				"%s.%s",
				typeName,
				name,
			)
		}
	}

	return nil
}

// RejectUnknownKeys fails when the object has a member the schema does not
// declare. Generated code calls it for schemas with additionalProperties false.
func RejectUnknownKeys(
	typeName string,
	members map[string]json.RawMessage,
	known ...string,
) error {
	for name := range members {
		if !slices.Contains(known, name) {
			return ctxerrors.Wrapf(
				commerr.ErrValidationFailed,
				"%s: unknown property %q",
				typeName,
				name,
			)
		}
	}

	return nil
}
