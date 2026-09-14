package engine

import "testing"

func TestValidateCredentials_Nil(t *testing.T) {
	username, password, err := validateCredentials(nil)
	if err != nil {
		t.Fatalf("validateCredentials(nil) error = %v", err)
	}
	if username != "" || password != "" {
		t.Errorf("validateCredentials(nil) = (%q, %q), want (\"\", \"\")", username, password)
	}
}

func TestValidateCredentials_Valid(t *testing.T) {
	username, password, err := validateCredentials(map[string]string{
		"username": "myuser",
		"password": "mypass",
	})
	if err != nil {
		t.Fatalf("validateCredentials() error = %v", err)
	}
	if username != "myuser" || password != "mypass" {
		t.Errorf("validateCredentials() = (%q, %q), want (myuser, mypass)", username, password)
	}
}

func TestValidateCredentials_MissingKey(t *testing.T) {
	_, _, err := validateCredentials(map[string]string{"username": "myuser"})
	if err == nil {
		t.Fatal("validateCredentials() error = nil, want error for missing password key")
	}
}

func TestValidateCredentials_ExtraKey(t *testing.T) {
	_, _, err := validateCredentials(map[string]string{
		"username": "myuser",
		"password": "mypass",
		"registry": "ghcr.io",
	})
	if err == nil {
		t.Fatal("validateCredentials() error = nil, want error for an extra key")
	}
}

func TestValidateCredentials_EmptyValue(t *testing.T) {
	_, _, err := validateCredentials(map[string]string{"username": "", "password": "mypass"})
	if err == nil {
		t.Fatal("validateCredentials() error = nil, want error for an empty username")
	}
}

func TestRegistryHostFor(t *testing.T) {
	cases := map[string]string{
		"node:20":                       "index.docker.io",
		"myuser/myimage:latest":         "index.docker.io",
		"ghcr.io/owner/image:tag":       "ghcr.io",
		"localhost:5000/image":          "localhost:5000",
		"my.registry.example.com/image": "my.registry.example.com",
	}
	for image, want := range cases {
		got := registryHostFor(image)
		if got != want {
			t.Errorf("registryHostFor(%q) = %q, want %q", image, got, want)
		}
	}
}
