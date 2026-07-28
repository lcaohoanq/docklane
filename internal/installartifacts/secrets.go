package installartifacts

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/bcrypt"
)

const (
	DashboardUsername      = "admin"
	dashboardPasswordBytes = 32
)

type DashboardCredentials struct {
	Username  string
	Password  []byte
	UsersFile []byte
}

func GenerateDashboardCredentials(
	random io.Reader,
) (DashboardCredentials, error) {
	if random == nil {
		random = rand.Reader
	}
	entropy := make([]byte, dashboardPasswordBytes)
	if _, err := io.ReadFull(random, entropy); err != nil {
		return DashboardCredentials{}, fmt.Errorf(
			"generate dashboard password: %w",
			err,
		)
	}
	defer clear(entropy)
	password := make(
		[]byte,
		base64.RawURLEncoding.EncodedLen(len(entropy)),
	)
	base64.RawURLEncoding.Encode(password, entropy)
	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		clear(password)
		return DashboardCredentials{}, fmt.Errorf(
			"hash dashboard password: %w",
			err,
		)
	}
	usersFile := make(
		[]byte,
		0,
		len(DashboardUsername)+1+len(hash)+1,
	)
	usersFile = append(usersFile, DashboardUsername...)
	usersFile = append(usersFile, ':')
	usersFile = append(usersFile, hash...)
	usersFile = append(usersFile, '\n')
	return DashboardCredentials{
		Username:  DashboardUsername,
		Password:  password,
		UsersFile: usersFile,
	}, nil
}

func (credentials DashboardCredentials) PasswordFile() []byte {
	content := make([]byte, 0, len(credentials.Password)+1)
	content = append(content, credentials.Password...)
	content = append(content, '\n')
	return content
}
