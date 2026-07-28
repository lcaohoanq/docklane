package installartifacts

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("injected random failure")
}

func TestGenerateDashboardCredentials(t *testing.T) {
	random := bytes.NewReader(bytes.Repeat([]byte{0x5a}, dashboardPasswordBytes))
	credentials, err := GenerateDashboardCredentials(random)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(credentials.Password)
	defer clear(credentials.UsersFile)

	if credentials.Username != DashboardUsername {
		t.Fatalf("username = %q", credentials.Username)
	}
	if len(credentials.Password) != 43 ||
		strings.ContainsAny(string(credentials.Password), ":\r\n") {
		t.Fatalf("unsafe password encoding = %q", credentials.Password)
	}
	passwordFile := credentials.PasswordFile()
	defer clear(passwordFile)
	if !bytes.Equal(
		passwordFile,
		append(append([]byte(nil), credentials.Password...), '\n'),
	) {
		t.Fatalf("password file = %q", passwordFile)
	}
	parts := bytes.Split(bytes.TrimSuffix(credentials.UsersFile, []byte{'\n'}), []byte{':'})
	if len(parts) != 2 || string(parts[0]) != DashboardUsername {
		t.Fatalf("users file = %q", credentials.UsersFile)
	}
	if err := bcrypt.CompareHashAndPassword(parts[1], credentials.Password); err != nil {
		t.Fatalf("bcrypt users file does not match password: %v", err)
	}
	cost, err := bcrypt.Cost(parts[1])
	if err != nil || cost < bcrypt.DefaultCost {
		t.Fatalf("bcrypt cost = %d, error = %v", cost, err)
	}
	if bytes.Contains(credentials.UsersFile, credentials.Password) {
		t.Fatal("users file exposed the raw password")
	}
}

func TestGenerateDashboardCredentialsPropagatesRandomFailure(t *testing.T) {
	if _, err := GenerateDashboardCredentials(failingReader{}); err == nil {
		t.Fatal("random failure was ignored")
	}
}
