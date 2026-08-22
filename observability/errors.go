package observability

import (
	"fmt"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PrepareAndLogError standardizes our error handling by logging, tracing, and formatting an error consistently.
func PrepareAndLogError(err error, logger logging.Logger, span tracing.Span, descriptionFmt string, descriptionArgs ...any) error {
	if err == nil {
		return nil
	}

	desc := fmt.Sprintf(descriptionFmt, descriptionArgs...)
	if span != nil {
		tracing.AttachErrorToSpan(span, desc, err)
	}

	if logger != nil {
		logger.Error(desc, err)
	}

	if desc != "" {
		return errors.Wrap(err, desc)
	}
	return err
}

// PrepareError standardizes our error handling by logging, tracing, and formatting an error consistently.
func PrepareError(err error, span tracing.Span, descriptionFmt string, descriptionArgs ...any) error {
	if err == nil {
		return nil
	}

	desc := fmt.Sprintf(descriptionFmt, descriptionArgs...)
	if span != nil {
		tracing.AttachErrorToSpan(span, desc, err)
	}

	if desc != "" {
		return errors.Wrap(err, desc)
	}
	return err
}

// unspecifiedErrorDescription stands in for a description a caller did not
// supply, so the error still reaches both pillars.
const unspecifiedErrorDescription = "unspecified error"

// AcknowledgeError standardizes our error handling by logging and tracing consistently.
//
// A nil error is nothing to acknowledge, and returns without emitting — matching
// PrepareError and PrepareAndLogError, which already return nil untouched.
//
// An empty description does not: this used to drop the error entirely, which
// made the one argument that is only ever decoration decide whether the error
// was reported at all.
func AcknowledgeError(err error, logger logging.Logger, span tracing.Span, descriptionFmt string, descriptionArgs ...any) {
	if err == nil {
		return
	}

	desc := fmt.Sprintf(descriptionFmt, descriptionArgs...)
	if desc == "" {
		desc = unspecifiedErrorDescription
	}

	logging.EnsureLogger(logger).Error(desc, err)
	tracing.AttachErrorToSpan(span, desc, err)
}

// PrepareAndLogGRPCStatus standardizes our error handling by logging, tracing, and formatting an error consistently.
func PrepareAndLogGRPCStatus(err error, logger logging.Logger, span tracing.Span, code codes.Code, descriptionFmt string, descriptionArgs ...any) error {
	if err == nil {
		return nil
	}

	desc := fmt.Sprintf(descriptionFmt, descriptionArgs...)
	if span != nil {
		tracing.AttachErrorToSpan(span, desc, err)
	}

	if logger != nil {
		logger.Error(desc, err)
	}

	// Wrap with platform/errors so the chain is wire-transmittable.
	wrapped := err
	if desc != "" {
		wrapped = errors.Wrap(err, desc)
	}
	return status.Errorf(code, "%v", wrapped)
}
