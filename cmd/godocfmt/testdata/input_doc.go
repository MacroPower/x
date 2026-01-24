// Package testdata provides comprehensive test cases for godocfmt. It demonstrates various doc comment patterns including long lines that need wrapping, code blocks, lists, headings, and doc links.
//
// This package is designed to test the full range of Go doc comment formatting capabilities. It includes examples of all supported syntax.
//
// # Features
//
// The formatter supports the following features:
//
//   - Line wrapping at configurable width
//   - Sentence-aware line breaking for improved readability
//   - Preservation of code blocks with proper indentation
//   - Support for bullet and numbered lists
//   - Doc links like [Foo] and [Type.Method]
//   - URL links with definitions like [RFC 7159]
//
// # Usage
//
// To use this package, import it and call the [New] function:
//
//	client := testdata.New()
//	defer client.Close()
//
// For more information, see [RFC 7159] which defines the JSON format.
//
// [RFC 7159]: https://tools.ietf.org/html/rfc7159
package testdata

import "errors"

// ErrNotFound is returned when an item cannot be located in the store. Check with errors.Is to handle this error appropriately.
var ErrNotFound = errors.New("not found")

// ErrInvalid indicates the input was malformed or failed validation. The caller should check the input and retry with valid data.
var ErrInvalid = errors.New("invalid")

// DefaultTimeout is the default timeout value in seconds. Use this when no explicit timeout is configured for operations.
const DefaultTimeout = 30

// MaxRetries defines the maximum number of retry attempts for transient failures. Set to zero to disable retries entirely.
const MaxRetries = 3

// Client represents a connection to a remote service. Use [New] to create a properly initialized instance. The client is safe for concurrent use.
//
// The Client handles connection pooling, retries, and timeout management automatically. Configure behavior using functional options.
//
// Example:
//
//	c := New(
//		WithTimeout(10*time.Second),
//		WithRetries(3),
//	)
//	defer c.Close()
//	result, err := c.Query("SELECT * FROM users")
type Client struct {
	// Timeout specifies the maximum duration for operations. Set to zero for no timeout. The default is [DefaultTimeout] seconds.
	Timeout int

	// MaxConns limits the connection pool size. Higher values allow more concurrent operations but consume more resources.
	MaxConns int

	// RetryPolicy configures automatic retry behavior. Use [WithRetries] to set this during construction.
	RetryPolicy *RetryConfig
}

// RetryConfig defines the retry behavior for failed operations. Create using [NewRetryConfig] for sensible defaults.
type RetryConfig struct {
	// MaxAttempts is the maximum number of retry attempts. Set to 1 for no retries.
	MaxAttempts int

	// BackoffMultiplier controls exponential backoff growth. A value of 2.0 doubles the delay between each attempt.
	BackoffMultiplier float64
}

// Option is a functional option for configuring a [Client]. Pass options to [New] to customize client behavior.
type Option func(*Client)

// New creates a new [Client] with the given options. It returns [ErrInvalid] if the configuration is invalid. The client must be closed when no longer needed.
//
// Options:
//
//  1. [WithTimeout] sets the operation timeout
//  2. [WithRetries] configures retry behavior
//  3. [WithMaxConns] limits connection pool size
//
// Example:
//
//	client := New(
//		WithTimeout(30 * time.Second),
//		WithRetries(3),
//	)
//	if client == nil {
//		log.Fatal("failed to create client")
//	}
func New(opts ...Option) *Client {
	return nil
}

// WithTimeout returns an [Option] that sets the client timeout. Values less than or equal to zero result in no timeout.
func WithTimeout(seconds int) Option {
	return nil
}

// WithRetries returns an [Option] that configures retry behavior. Pass zero to disable retries. Negative values are treated as zero.
func WithRetries(max int) Option {
	return nil
}

// WithMaxConns returns an [Option] that limits the connection pool. The default pool size is 10 connections.
func WithMaxConns(n int) Option {
	return nil
}

// Close releases all resources held by the [Client]. It returns an error if the client is already closed or if cleanup fails. Always defer Close after creating a client.
func (c *Client) Close() error {
	return nil
}

// Query executes a query and returns the results. It returns [ErrNotFound] if no results match. The query string must be non-empty.
//
// Query supports parameterized queries to prevent injection:
//
//	results, err := client.Query("SELECT * FROM users WHERE id = ?", userID)
//	if errors.Is(err, ErrNotFound) {
//		// Handle missing data
//	}
//
// For complex queries, consider using [Client.QueryContext] which provides cancellation support.
func (c *Client) Query(query string, args ...any) ([]any, error) {
	return nil, nil
}

// QueryContext is like [Client.Query] but accepts a context for cancellation. The context deadline takes precedence over the client timeout.
func (c *Client) QueryContext(query string, args ...any) ([]any, error) {
	return nil, nil
}

// Ping checks connectivity to the remote service. It returns nil if the connection is healthy. Use this for health checks.
func (c *Client) Ping() error {
	return nil
}

// NewRetryConfig creates a [RetryConfig] with sensible defaults. It configures 3 attempts with exponential backoff.
func NewRetryConfig() *RetryConfig {
	return nil
}

// Helper is an internal utility type. It should not be used directly by external packages.
type Helper struct{}

//go:generate stringer -type=Status
type Status int

//nolint:errcheck
func uncheckedHelper() {}

// processData handles the data processing pipeline. This is a very long function description that should wrap to multiple lines when formatted at the default width of 80 characters to demonstrate line wrapping behavior.
func processData() {}

// validateInput checks if the input meets requirements. Returns [ErrInvalid] if validation fails. The validation includes checking length, format, and semantic correctness of the provided data.
func validateInput() error {
	return nil
}

// Deprecated: Use [New] instead. OldNew is the legacy constructor that lacks proper option support.
func OldNew() *Client {
	return nil
}

// complexOperation performs a multi-step operation. It involves the following steps:
//
//   - Step 1: Validate input parameters
//   - Step 2: Establish connection to remote service
//   - Step 3: Execute the primary operation
//   - Step 4: Parse and validate response
//   - Step 5: Clean up resources
//
// Each step may fail independently. Errors from any step are wrapped with context.
//
// Code example showing error handling:
//
//	err := complexOperation()
//	if err != nil {
//		var connErr *ConnectionError
//		if errors.As(err, &connErr) {
//			// Handle connection failure
//		}
//	}
func complexOperation() error {
	return nil
}
