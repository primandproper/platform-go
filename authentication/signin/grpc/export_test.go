package grpc

import (
	"google.golang.org/grpc"
)

// CookieRole is what RefreshTokenCookie does on one method: seal the token its
// response issues, fill the refresh token its request spends, and delete the
// cookie once it succeeds.
type CookieRole struct {
	Issues bool
	Spends bool
	Ends   bool
}

// CookieRoles exposes the descriptor walk to the suite's pin of which methods
// it covers.
func CookieRoles(services map[string]grpc.ServiceInfo) (map[string]CookieRole, error) {
	methods, err := cookieMethods(services)
	if err != nil {
		return nil, err
	}

	roles := make(map[string]CookieRole, len(methods))
	for name := range methods {
		roles[name] = CookieRole{Issues: methods[name].issues, Spends: methods[name].refreshToken != nil, Ends: methods[name].ends}
	}

	return roles, nil
}
