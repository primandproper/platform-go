package observability

import (
	"fmt"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/tracing"

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

// AcknowledgeError standardizes our error handling by logging and tracing consistently.
func AcknowledgeError(err error, logger logging.Logger, span tracing.Span, descriptionFmt string, descriptionArgs ...any) {
	if desc := fmt.Sprintf(descriptionFmt, descriptionArgs...); desc != "" {
		logging.EnsureLogger(logger).Error(desc, err)
		if span != nil {
			tracing.AttachErrorToSpan(span, desc, err)
		}
	}
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
