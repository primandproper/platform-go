package keys

const (
	idSuffix = ".id"

	// NameKey is the standard key for referring to a name.
	NameKey = "name"
	// SpanIDKey is the standard key for referring to a span ID.
	SpanIDKey = "span" + idSuffix
	// TraceIDKey is the standard key for referring to a trace ID.
	TraceIDKey = "trace" + idSuffix
	// FilterCreatedAfterKey is the standard key for referring to a types.QueryFilter's CreatedAfter field.
	FilterCreatedAfterKey = "query_filter.created_after"
	// FilterCreatedBeforeKey is the standard key for referring to a types.QueryFilter's CreatedBefore field.
	FilterCreatedBeforeKey = "query_filter.created_before"
	// FilterUpdatedAfterKey is the standard key for referring to a types.QueryFilter's UpdatedAfter field.
	FilterUpdatedAfterKey = "query_filter.updated_after"
	// FilterUpdatedBeforeKey is the standard key for referring to a types.QueryFilter's UpdatedAfter field.
	FilterUpdatedBeforeKey = "query_filter.updated_before"
	// FilterSortByKey is the standard key for referring to a types.QueryFilter's SortBy field.
	FilterSortByKey = "query_filter.sort_by"
	// FilterCursorKey is the standard key for referring to a types.QueryFilter's next cursor.
	FilterCursorKey = "query_filter.cursor"
	// FilterLimitKey is the standard key for referring to a types.QueryFilter's limit.
	FilterLimitKey = "query_filter.limit"
	// FilterIsNilKey is the standard key for referring to a types.QueryFilter's null status.
	FilterIsNilKey = "query_filter.is_nil"
	// URLKey is the standard key for referring to a URL.
	URLKey = "url"
	// RequestHeadersKey is the standard key for referring to a http.Request's Headers.
	RequestHeadersKey = "request.headers"
	// RequestIDKey is the standard key for referring to a http.Request's ID.
	RequestIDKey = "request" + idSuffix
	// RequestMethodKey is the standard key for referring to a http.Request's Method.
	RequestMethodKey = "request.method"
	// RequestURIKey is the standard key for referring to a http.Request's URI.
	RequestURIKey = "request.uri"
	// ResponseStatusKey is the standard key for referring to a http.Request's status.
	ResponseStatusKey = "response.status"
	// ResponseHeadersKey is the standard key for referring to a http.Response's Headers.
	ResponseHeadersKey = "response.headers"
	// ReasonKey is the standard key for referring to a reason for a change.
	ReasonKey = "reason"
	// URLQueryKey is the standard key for referring to a URL query.
	URLQueryKey = "url.query"
	// SearchQueryKey is the standard key for referring to a search query parameter value.
	SearchQueryKey = "search_query"
	// UserAgentOSKey is the standard key for referring to a user agent's OS.
	UserAgentOSKey = "os"
	// UserAgentBotKey is the standard key for referring to a user agent's bot status.
	UserAgentBotKey = "is_bot"
	// UserAgentMobileKey is the standard key for referring to user agent's mobile status.
	UserAgentMobileKey = "is_mobile"
	// ValidationErrorKey is the standard key for referring to a struct validation error.
	ValidationErrorKey = "validation_error"
	// IndexNameKey is the standard key for referring to a given search index.
	IndexNameKey = "index.name"
	// UseDatabaseKey is the standard key for referring to whether or not the database was used in search.
	UseDatabaseKey = "use_database"

	// RequesterIDKey is the standard key for referring to a requesting user's ID (session/request context).
	RequesterIDKey = "request.made_by"
	// ActiveAccountIDKey is the standard key for referring to an active account ID (session context).
	ActiveAccountIDKey = "active_account" + idSuffix
	// UserIsServiceAdminKey is the standard key for referring to a user's admin status (session context).
	UserIsServiceAdminKey = "user.is_admin"
	// UserIDKey is the standard key for referring to a user ID (request/session context).
	UserIDKey = "user" + idSuffix
	// UsernameKey is the standard key for referring to a username (request context).
	UsernameKey = "user.username"

	// EmailSubjectKey is the standard key for referring to an outbound email's subject.
	EmailSubjectKey = "email.subject"
	// EmailToAddressKey is the standard key for referring to an outbound email's recipient address.
	EmailToAddressKey = "email.to_address"
	// EmailFromAddressKey is the standard key for referring to an outbound email's sender address.
	EmailFromAddressKey = "email.from_address"
	// FilenameKey is the standard key for referring to a filename.
	FilenameKey = "filename"
	// LengthKey is the standard key for referring to a requested or measured length.
	LengthKey = "length"
	// ConnectionURLKey is the standard key for referring to a datastore connection URL.
	ConnectionURLKey = "connection_url"
	// TopicKey is the standard key for referring to a message queue topic.
	TopicKey = "topic"
)
