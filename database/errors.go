package database

import (
	platformerrors "github.com/primandproper/platform-go/v3/errors"
)

// ErrUserAlreadyExists indicates that a user with that username has already been created.
var ErrUserAlreadyExists = platformerrors.New("user already exists")
