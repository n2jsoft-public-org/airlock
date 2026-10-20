package mcpserve

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
)

// CredentialsError is a failure to read the profile's credentials, typically an expired sign-in.
type CredentialsError struct{ Err error }

// Error never carries the SDK's chain ("operation error …, https response error …"): only the
// API error code when there is one. The cause stays reachable through Unwrap.
func (e *CredentialsError) Error() string {
	msg := "failed to read the AWS credentials of the profile (sign in again from airlock)"
	if apiErr, ok := errors.AsType[smithy.APIError](e.Err); ok {
		msg += ": " + apiErr.ErrorCode()
	}
	return msg
}

func (e *CredentialsError) Unwrap() error { return e.Err }

// SignInCredentials makes every failure of p a *CredentialsError, which AWSError turns into a
// "sign in again" message instead of the SDK's operation chain.
func SignInCredentials(p aws.CredentialsProvider) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		creds, err := p.Retrieve(ctx)
		if err != nil {
			return aws.Credentials{}, &CredentialsError{Err: err}
		}
		return creds, nil
	})
}
