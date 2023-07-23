package env

import "os"

// Provider represents an entity that is able to provide environment variables.
type Provider interface {
	// LookupEnv retrieves the value of the environment variable named by the key.
	// If it is not found, the boolean will be false.
	LookupEnv(key string) (value string, ok bool)
}

// ProviderFunc is an adapter that allows using a function as [Provider].
type ProviderFunc func(key string) (value string, ok bool)

// LookupEnv implements the [Provider] interface.
func (f ProviderFunc) LookupEnv(key string) (string, bool) { return f(key) }

// OS is the main [Provider] that uses [os.LookupEnv].
var OS Provider = ProviderFunc(os.LookupEnv)

// Map is an in-memory [Provider] implementation useful in tests.
type Map map[string]string

// LookupEnv implements the [Provider] interface.
func (m Map) LookupEnv(key string) (string, bool) {
	value, ok := m[key]
	return value, ok
}

// MultiProvider combines multiple providers into a single one containing the union of all environment variables.
// The order of the given providers matters: if the same key occurs more than once, the later value takes precedence.
func MultiProvider(ps ...Provider) Provider { return providers(ps) }

type providers []Provider

func (ps providers) LookupEnv(key string) (string, bool) {
	var value string
	var found bool
	for _, p := range ps {
		if v, ok := p.LookupEnv(key); ok {
			value = v
			found = true
		}
	}
	return value, found
}
