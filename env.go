// Package env provides an API for loading environment variables into structs.
package env

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
)

var (
	// ErrInvalidArgument is returned when the argument provided to
	// Load/LoadFrom is invalid.
	ErrInvalidArgument = errors.New("env: argument must be a non-nil struct pointer")

	// ErrEmptyTagName is returned when the `env` tag is found but the name of
	// the environment variable is empty.
	ErrEmptyTagName = errors.New("env: empty tag name is not allowed")

	// ErrUnsupportedType is returned when the provided struct contains a field
	// of an unsupported type.
	ErrUnsupportedType = errors.New("env: unsupported type")

	// ErrInvalidTagOption is returned when the `env` tag contains an invalid
	// option, e.g. `env:"VAR,foo"`.
	ErrInvalidTagOption = errors.New("env: invalid tag option")
)

// NotSetError is returned when environment variables are marked as required but
// not set.
type NotSetError struct {
	// Names is a slice of the names of the missing required environment
	// variables.
	Names []string
}

// Error implements the error interface.
func (e *NotSetError) Error() string {
	return fmt.Sprintf("env: %v are required but not set", e.Names)
}

// Load loads environment variables into the provided struct using the OS
// Provider as their source. To specify a custom Provider, use LoadFrom
// function. dst must be a non-nil struct pointer, otherwise Load returns
// ErrInvalidArgument.
//
// The struct fields must have the `env:"VAR"` struct tag, where VAR is the name
// of the corresponding environment variable. Unexported fields and fields
// without this tag (except nested structs) are ignored. If the tag is found but
// the name of the environment variable is empty, the error will be
// ErrEmptyTagName.
//
// The following types are supported as struct fields:
//  int (any kind)
//  float (any kind)
//  bool
//  string
//  time.Duration
//  encoding.TextUnmarshaler
//  slices of any type above (space is the default separator for values)
// See the strconv package from the standard library for parsing rules.
// Implementing the encoding.TextUnmarshaler interface is enough to use any
// user-defined type. Default values can be specified using basic struct
// initialization. They will be left untouched, if no corresponding environment
// variables are found. Nested structs of any depth level are supported, but
// only non-struct fields are considered as targets for parsing. If a field of
// an unsupported type is found, the error will be ErrUnsupportedType.
//
// The name of the environment variable can be followed by comma-separated
// options in the form of `env:"VAR,option1,option2,..."`. The following
// tag-level options are supported:
//  required: mark the environment variable as required
//  expand:   expand the value of the environment variable using os.Expand
// If environment variables are marked as required but not set, an error of type
// NotSetError will be returned. If the tag contains an invalid option, the
// error will be ErrInvalidTagOption.
//
// In addition to the tag-level options, Load also supports the following
// function-level options:
//  WithPrefix:         set prefix for each environment variable
//  WithSliceSeparator: set custom separator to parse slice values
// See their documentation for details.
func Load(dst interface{}, opts ...Option) error {
	return newLoader(OS, opts...).loadVars(dst)
}

// LoadFrom loads environment variables into the provided struct using the
// specified Provider as their source. See Load documentation for more details.
func LoadFrom(p Provider, dst interface{}, opts ...Option) error {
	return newLoader(p, opts...).loadVars(dst)
}

// Option allows to customize the behaviour of Load/LoadFrom functions.
type Option func(*loader)

// WithPrefix configures Load/LoadFrom to automatically add the provided prefix
// to each environment variable. By default, no prefix is configured.
func WithPrefix(prefix string) Option {
	return func(l *loader) { l.prefix = prefix }
}

// WithSliceSeparator configures Load/LoadFrom to use the provided separator
// when parsing slice values. The default one is space.
func WithSliceSeparator(sep string) Option {
	return func(l *loader) { l.sliceSep = sep }
}

// loader is an environment variables loader.
type loader struct {
	provider Provider
	prefix   string
	sliceSep string
}

// newLoader creates a new loader with the specified Provider and applies the
// provided options, which override the default settings.
func newLoader(p Provider, opts ...Option) *loader {
	l := loader{
		provider: p,
		prefix:   "",
		sliceSep: " ",
	}
	for _, opt := range opts {
		opt(&l)
	}
	return &l
}

// loadVars loads environment variables into the provided struct.
func (l *loader) loadVars(dst interface{}) error {
	rv := reflect.ValueOf(dst)
	if !structPtr(rv) {
		return ErrInvalidArgument
	}

	vars, err := l.parseVars(rv.Elem())
	if err != nil {
		return err
	}

	// accumulate missing required variables
	// to return NotSetError after the iteration is finished.
	var notset []string

	for _, v := range vars {
		value, ok := l.lookupEnv(v.name, v.expand)
		if !ok {
			if v.required {
				notset = append(notset, v.name)
			}
			continue
		}

		var err error
		if kindOf(v.field, reflect.Slice) && !implements(v.field, unmarshalerIface) {
			err = setSlice(v.field, strings.Split(value, l.sliceSep))
		} else {
			err = setValue(v.field, value)
		}
		if err != nil {
			return err
		}
	}

	if len(notset) > 0 {
		return &NotSetError{Names: notset}
	}

	return nil
}

// parseVars parses environment variables from the fields of the provided
// struct.
func (l *loader) parseVars(v reflect.Value) ([]variable, error) {
	var vars []variable

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if !field.CanSet() {
			// skip unexported fields.
			continue
		}

		// special case: a nested struct, parse its fields recursively.
		if kindOf(field, reflect.Struct) && !implements(field, unmarshalerIface) {
			nested, err := l.parseVars(field)
			if err != nil {
				return nil, err
			}
			vars = append(vars, nested...)
			continue
		}

		sf := v.Type().Field(i)
		value, ok := sf.Tag.Lookup("env")
		if !ok {
			// skip fields without the `env` tag.
			continue
		}

		parts := strings.Split(value, ",")
		name, options := parts[0], parts[1:]
		if name == "" {
			return nil, ErrEmptyTagName
		}

		var required, expand bool
		for _, option := range options {
			switch option {
			case "required":
				required = true
			case "expand":
				expand = true
			default:
				return nil, fmt.Errorf("%w %q", ErrInvalidTagOption, option)
			}
		}

		vars = append(vars, variable{
			name:     l.prefix + name,
			required: required,
			expand:   expand,
			field:    field,
		})
	}

	return vars, nil
}

// lookupEnv retrieves the value of the environment variable named by the key
// using the internal Provider. It replaces $VAR or ${VAR} in the result using
// os.Expand if expand is true.
func (l *loader) lookupEnv(key string, expand bool) (string, bool) {
	value, ok := l.provider.LookupEnv(key)
	if !ok {
		return "", false
	}

	if !expand {
		return value, true
	}

	mapping := func(key string) string {
		v, _ := l.provider.LookupEnv(key)
		return v
	}

	return os.Expand(value, mapping), true
}

// variable contains information about an environment variable parsed from a
// struct field.
type variable struct {
	name     string
	required bool
	expand   bool
	field    reflect.Value // the original struct field.
}