package oauth2server

import (
	"context"
	"html/template"
	"net/http"
)

// The form field names the shipped login page posts, and that the shipped
// SubjectAuthenticator adapters read.
//
// They are exported so an application replacing the renderer but not the
// authenticator — or the other way round — has one spelling to agree on rather
// than two string literals in two files.
const (
	FieldUsername = "username"
	FieldPassword = "password"
	FieldTOTPCode = "totp_code"
)

// FieldClientID is the client_id parameter of an authorization request, under
// the name a seam reads it off the parsed form with.
//
// It is here rather than only in this package's internal parameter list because
// a SubjectAuthenticator or SubjectResolver that has to know which client the
// request named — authentication/oauth2clients/authserver is the one in this
// module — otherwise spells the literal itself, which is the second spelling
// the three above exist to avoid. It is an alias for that internal name rather
// than a second constant, so the two cannot disagree.
const FieldClientID = paramClientID

// DefaultLoginRenderer is the login page this package ships.
//
// It exists because the alternative is a package that implements the whole of
// OAuth 2.1 and then asks every consumer to write an HTML form before any of it
// runs. It is deliberately unstyled and deliberately small: an application that
// cares what its login page looks like passes WithLoginRenderer, and one that
// does not gets a page that works with no stylesheet, no JavaScript, and no
// assets to serve.
//
// It renders through html/template, so the client name — which is
// attacker-supplied, since registration is open — is escaped rather than
// trusted.
var DefaultLoginRenderer LoginRenderer = LoginRendererFunc(renderDefaultLogin)

// loginTemplate is parsed once at init. A parse failure here is a bug in this
// file rather than anything a deployment can cause, so Must is right: it fails
// at process start rather than on the first login.
var loginTemplate = template.Must(template.New("oauth2server_login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in</title>
</head>
<body>
<main>
<h1>Sign in</h1>
<p>{{.ClientName}} is requesting access{{if .Scopes}} to: {{range $i, $s := .Scopes}}{{if $i}}, {{end}}{{$s}}{{end}}{{end}}.</p>
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}
<form method="post" action="{{.Action}}" autocomplete="off">
<p><label for="username">Username</label><br>
<input id="username" name="` + FieldUsername + `" type="text" autocomplete="username" required autofocus></p>
<p><label for="password">Password</label><br>
<input id="password" name="` + FieldPassword + `" type="password" autocomplete="current-password" required></p>
<p><label for="totp_code">Authentication code</label><br>
<input id="totp_code" name="` + FieldTOTPCode + `" type="text" inputmode="numeric" autocomplete="one-time-code"></p>
<p><button type="submit">Sign in</button></p>
</form>
</main>
</body>
</html>
`))

// renderDefaultLogin writes the shipped form.
//
// The cache headers are not decoration. A login page cached by a shared proxy
// is a login page served to the next person, and the URL carries the whole
// authorization request in its query string.
func renderDefaultLogin(_ context.Context, res http.ResponseWriter, view LoginView) {
	res.Header().Set("Content-Type", "text/html; charset=utf-8")
	res.Header().Set("Cache-Control", "no-store")
	res.Header().Set("Pragma", "no-cache")

	// A failed login is answered 401 rather than 200, so that a client driving
	// this without a browser can tell the two renders apart.
	status := http.StatusOK
	if view.Error != "" {
		status = http.StatusUnauthorized
	}

	res.WriteHeader(status)

	// Nothing useful can be done with a write failure here: the status and part
	// of the body are already on the wire, so there is no error page left to
	// send. The client will see a truncated response, which is what a dropped
	// connection looks like anyway.
	//nolint:errcheck // see above: the status and part of the body are already sent.
	_ = loginTemplate.Execute(res, view)
}
