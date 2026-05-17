package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVaultEncryptDecrypt(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")

	v, err := OpenVault(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	plain := "s3cret_p@ssw0rd!"
	encrypted, err := v.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == plain {
		t.Error("encrypted should differ from plaintext")
	}

	decrypted, err := v.Decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != plain {
		t.Errorf("decrypted = %q, want %q", decrypted, plain)
	}
}

func TestVaultEmptyString(t *testing.T) {
	dir := t.TempDir()
	v, err := OpenVault(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}

	enc, err := v.Encrypt("")
	if err != nil {
		t.Fatal(err)
	}
	if enc != "" {
		t.Errorf("Encrypt('') = %q, want empty", enc)
	}

	dec, err := v.Decrypt("")
	if err != nil {
		t.Fatal(err)
	}
	if dec != "" {
		t.Errorf("Decrypt('') = %q, want empty", dec)
	}
}

func TestVaultKeyPersistence(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")

	v1, err := OpenVault(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	encrypted, err := v1.Encrypt("testpassword")
	if err != nil {
		t.Fatal(err)
	}

	v2, err := OpenVault(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	decrypted, err := v2.Decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != "testpassword" {
		t.Errorf("decrypted = %q, want %q", decrypted, "testpassword")
	}
}

func TestVaultKeyFileCreated(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")

	_, err := OpenVault(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if len(data) != 32 {
		t.Errorf("key size = %d, want 32", len(data))
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Errorf("key file permissions too open: %v", info.Mode().Perm())
	}
}
